//go:build windows

package headless

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	systemTimeOfDayInformation = 3
	tokenSourceClass           = 7
	reportInterval             = 2 * time.Second
	sampleInterval             = time.Second
	// maxIgnored bounds the processes of a watched image that are not
	// generations, held only so their PIDs cannot be reused.
	maxIgnored = 256
	maxMarks   = 32
	// crashExitCode is the exit code the observer gives a process it
	// terminates.
	crashExitCode = 0xdead
)

// heldProcess is one process the observer holds. While the observer holds
// its handle, its PID cannot name another process.
type heldProcess struct {
	h   windows.Handle
	gen int // index into the report's generations, or -1
}

type observer struct {
	cfg        ObserveConfig
	rep        ObserverReport
	held       map[uint32]*heldProcess
	byGen      map[int]*heldProcess
	self       uint32
	last       uint64
	daemon     string
	work       string
	nextOut    time.Time
	nextSample time.Time
	logons     map[string]bool
}

// runObserve is the SYSTEM observer. It launches nothing: it holds every
// process of the watched images, terminates exactly the generations its
// plan names, timestamps marks and creates the declared release file. It
// writes a running report as it goes and a finished one at the end.
func runObserve(args []string) error {
	cfg, err := parseObserve(args)
	if err != nil {
		return err
	}
	o := &observer{cfg: cfg, held: map[uint32]*heldProcess{}, byGen: map[int]*heldProcess{}, logons: map[string]bool{}, self: windows.GetCurrentProcessId(),
		daemon: filepath.Base(cfg.DaemonImage), work: filepath.Base(cfg.WorkloadImage)}
	o.rep = ObserverReport{Schema: ObserverSchema, Accounts: cfg.Accounts, Plan: cfg.Plan(), Stage: ObserverRunning}
	defer o.close()
	if err := o.start(); err != nil {
		return o.fail(err)
	}
	deadline := time.Now().Add(cfg.Duration)
	for {
		if err := o.scan(); err != nil {
			return o.fail(err)
		}
		if time.Now().After(deadline) || o.stopRequested() {
			break
		}
		if time.Now().After(o.nextOut) {
			if err := writeJSONFile(o.cfg.Report, o.rep); err != nil {
				return o.fail(err)
			}
			o.nextOut = time.Now().Add(reportInterval)
		}
		time.Sleep(cfg.Scan)
	}
	if err := o.scan(); err != nil {
		return o.fail(err)
	}
	if err := o.sample(true); err != nil {
		return o.fail(err)
	}
	o.accountFacts()
	// After the last scan's exit checks, so every recorded exit is inside
	// the observation.
	o.rep.Ended = filetimeNow()
	o.rep.Stage = ObserverFinished
	return writeJSONFile(o.cfg.Report, o.rep)
}

func (o *observer) start() error {
	run, err := LoadAdmission(o.cfg.Admission)
	if err != nil {
		return err
	}
	exe, err := ExecutableSHA256()
	if err != nil {
		return err
	}
	if want := run.Manifest.Lookup(workloadImage); want == "" || exe != want {
		return errors.New("the observer is not the admitted " + workloadImage)
	}
	o.rep.Executable = exe
	o.rep.Boot, err = bootIdentity()
	return err
}

func (o *observer) fail(err error) error {
	o.rep.Stage, o.rep.Failure = ObserverFailed, err.Error()
	o.rep.Ended = filetimeNow()
	return errors.Join(err, writeJSONFile(o.cfg.Report, o.rep))
}

func (o *observer) close() {
	for _, p := range o.held {
		_ = windows.CloseHandle(p.h)
	}
}

func (o *observer) stopRequested() bool {
	if o.cfg.StopFile == "" {
		return false
	}
	_, err := os.Stat(o.cfg.StopFile)
	return err == nil
}

func filetimeNow() uint64 {
	var ft windows.Filetime
	windows.GetSystemTimeAsFileTime(&ft)
	return uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime)
}

// bootIdentity reads the kernel boot time and the boot counter.
func bootIdentity() (Boot, error) {
	var tod struct {
		BootTime      int64
		CurrentTime   int64
		TimeZoneBias  int64
		TimeZoneID    uint32
		Reserved      uint32
		BootTimeBias  uint64
		SleepTimeBias uint64
	}
	var n uint32
	if err := windows.NtQuerySystemInformation(systemTimeOfDayInformation, unsafe.Pointer(&tod), uint32(unsafe.Sizeof(tod)), &n); err != nil {
		return Boot{}, fmt.Errorf("boot time: %w", err)
	}
	b := Boot{Time: uint64(tod.BootTime)}
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager\Memory Management\PrefetchParameters`, registry.QUERY_VALUE)
	if err == nil {
		if v, _, err := k.GetIntegerValue("BootId"); err == nil {
			b.Counter = uint32(v)
		}
		_ = k.Close()
	}
	return b, nil
}

type candidate struct {
	pid, ppid uint32
	h         windows.Handle
	created   uint64
}

// scan holds new processes of the watched images, records exits, applies
// the crash plan, the release and marks.
func (o *observer) scan() error {
	cands, err := o.snapshot()
	if err != nil {
		return err
	}
	now := filetimeNow()
	if o.rep.Scans == 0 {
		o.rep.Started = now
	} else if gap := now - o.last; gap > o.rep.MaxGap {
		o.rep.MaxGap = gap
	}
	o.last = now
	o.rep.Scans++
	sort.Slice(cands, func(i, j int) bool { return cands[i].created < cands[j].created })
	for _, c := range cands {
		if err := o.classify(c, now); err != nil {
			return err
		}
	}
	if err := o.exits(); err != nil {
		return err
	}
	if err := o.crashDue(); err != nil {
		return err
	}
	if err := o.release(); err != nil {
		return err
	}
	if err := o.marks(); err != nil {
		return err
	}
	return o.sample(false)
}

// sample records the interactive sessions with a user when they change and
// the watched accounts' password-bearing logon sessions, about once a
// second and at the end.
func (o *observer) sample(final bool) error {
	if !final && time.Now().Before(o.nextSample) {
		return nil
	}
	o.nextSample = time.Now().Add(sampleInterval)
	users, err := interactiveUsers()
	if err != nil {
		return err
	}
	at := filetimeNow()
	if n := len(o.rep.Sessions); n == 0 || !slices.Equal(o.rep.Sessions[n-1].Users, users) {
		if n >= MaxGenerations {
			return errors.New("too many session changes")
		}
		o.rep.Sessions = append(o.rep.Sessions, SessionSample{At: at, Users: users})
	}
	sessions, err := logonSessions()
	if err != nil {
		return err
	}
	watched := map[string]bool{}
	for _, sid := range o.cfg.Accounts {
		watched[sid] = true
	}
	for _, l := range sessions {
		if watched[l.SID] && passwordLogon(l.Type) && !o.logons[l.ID] && len(o.rep.Logons) < maxIgnored {
			o.logons[l.ID] = true
			o.rep.Logons = append(o.rep.Logons, l)
		}
	}
	return nil
}

// accountFacts reads each watched account's profile and workload progress
// at the end of the observation.
func (o *observer) accountFacts() {
	o.rep.Profiles, o.rep.Progress = map[string]ProfileFacts{}, map[string]Progress{}
	for account, sid := range o.cfg.Accounts {
		facts, dir := profileFacts(sid)
		o.rep.Profiles[account] = facts
		if dir == "" {
			continue
		}
		if p, ok := workloadProgress(dir); ok && p.SID == sid {
			o.rep.Progress[account] = p
		}
	}
}

// snapshot opens every new process of a watched image. One that cannot be
// opened is recorded as unidentified.
func (o *observer) snapshot() ([]candidate, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("process snapshot: %w", err)
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	var out []candidate
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		name := windows.UTF16ToString(e.ExeFile[:])
		if e.ProcessID == o.self || o.held[e.ProcessID] != nil || !strings.EqualFold(name, o.daemon) && !strings.EqualFold(name, o.work) {
			continue
		}
		access := uint32(windows.SYNCHRONIZE | windows.PROCESS_QUERY_LIMITED_INFORMATION)
		if o.crashImage(name) {
			access |= windows.PROCESS_TERMINATE
		}
		h, oerr := windows.OpenProcess(access, false, e.ProcessID)
		if oerr != nil {
			o.unidentified(e.ProcessID, oerr)
			continue
		}
		created, cerr := creationTime(h)
		if cerr != nil {
			_ = windows.CloseHandle(h)
			o.unidentified(e.ProcessID, cerr)
			continue
		}
		out = append(out, candidate{pid: e.ProcessID, ppid: e.ParentProcessID, h: h, created: created})
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		for _, c := range out {
			_ = windows.CloseHandle(c.h)
		}
		return nil, fmt.Errorf("process snapshot: %w", err)
	}
	return out, nil
}

func (o *observer) crashImage(name string) bool {
	switch o.cfg.Crash {
	case RoleBroker, RoleManager:
		return strings.EqualFold(name, o.daemon)
	case RoleWorkload:
		return strings.EqualFold(name, o.work)
	}
	return false
}

func (o *observer) unidentified(pid uint32, err error) {
	if len(o.rep.Unidentified) < maxIgnored {
		o.rep.Unidentified = append(o.rep.Unidentified, Unidentified{PID: pid, At: filetimeNow(), Win32: win32Code(err)})
	}
}

// classify holds c as a generation of a watched role, or as an ignored
// process of a watched image.
func (o *observer) classify(c candidate, now uint64) error {
	if len(o.held) >= MaxGenerations+maxIgnored {
		_ = windows.CloseHandle(c.h)
		return errors.New("too many processes of the watched images")
	}
	p := &heldProcess{h: c.h, gen: -1}
	o.held[c.pid] = p
	image, err := imagePath(c.h)
	if err != nil {
		o.unidentified(c.pid, err)
		return nil
	}
	isDaemon := strings.EqualFold(image, o.cfg.DaemonImage)
	if !isDaemon && !strings.EqualFold(image, o.cfg.WorkloadImage) {
		return nil
	}
	tok, err := tokenFactsOf(c.h)
	if err != nil {
		o.unidentified(c.pid, err)
		return nil
	}
	g := Generation{PID: c.pid, ParentPID: c.ppid, Created: c.created, Seen: now, Token: tok}
	account := ""
	for role, sid := range o.cfg.Accounts {
		if sid == tok.SID {
			account = role
		}
	}
	switch {
	case isDaemon && tok.SID == SystemSID:
		broker, err := isServiceProcess(c.pid)
		if err != nil {
			return err
		}
		if !broker {
			return nil
		}
		g.Role = RoleBroker
	case account == "":
		return nil
	case isDaemon:
		g.Role, g.Account = RoleManager, account
	default:
		g.Role, g.Account = RoleChild, account
		if parent := o.held[c.ppid]; parent != nil && parent.gen >= 0 {
			pg := o.rep.Generations[parent.gen]
			if pg.Account == account && pg.Created <= c.created && pg.Role == RoleManager {
				g.Role = RoleWorkload
			}
		}
	}
	if len(o.rep.Generations) >= MaxGenerations {
		return errors.New("too many generations")
	}
	p.gen = len(o.rep.Generations)
	o.byGen[p.gen] = p
	o.rep.Generations = append(o.rep.Generations, g)
	return nil
}

// exits records the kernel exit time and code of every held generation
// that has exited.
func (o *observer) exits() error {
	for _, p := range o.held {
		if p.gen < 0 || o.rep.Generations[p.gen].Exited != 0 {
			continue
		}
		if ev, err := windows.WaitForSingleObject(p.h, 0); err != nil {
			return err
		} else if ev != windows.WAIT_OBJECT_0 {
			continue
		}
		if err := o.recordExit(p); err != nil {
			return err
		}
	}
	return nil
}

func (o *observer) recordExit(p *heldProcess) error {
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(p.h, &created, &exited, &kernel, &user); err != nil {
		return err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(p.h, &code); err != nil {
		return err
	}
	g := &o.rep.Generations[p.gen]
	g.Exited, g.ExitCode = uint64(exited.HighDateTime)<<32|uint64(exited.LowDateTime), code
	return nil
}

// crashDue terminates each running generation of the crash role whose
// planned life, counted from its creation or the first scan, has passed.
func (o *observer) crashDue() error {
	if o.cfg.Crash == "" {
		return nil
	}
	account := o.cfg.Target
	if o.cfg.Crash == RoleBroker {
		account = ""
	}
	var gens []int
	for i, g := range o.rep.Generations {
		if g.Role == o.cfg.Crash && g.Account == account {
			gens = append(gens, i)
		}
	}
	sort.Slice(gens, func(i, j int) bool { return o.rep.Generations[gens[i]].Created < o.rep.Generations[gens[j]].Created })
	for index, gi := range gens {
		g := &o.rep.Generations[gi]
		life, crash := o.cfg.LifeOf(index)
		if !crash || g.Exited != 0 || g.Crashed != 0 {
			continue
		}
		since := max(g.Created, o.rep.Started)
		if filetimeNow()-since < uint64(life/100) {
			continue
		}
		p := o.byGen[gi]
		g.Crashed = filetimeNow()
		if err := windows.TerminateProcess(p.h, crashExitCode); err != nil {
			return fmt.Errorf("terminate %s %d: %w", g.Role, g.PID, err)
		}
		if ev, err := windows.WaitForSingleObject(p.h, 5000); err != nil || ev != windows.WAIT_OBJECT_0 {
			return fmt.Errorf("%s %d did not exit after termination", g.Role, g.PID)
		}
		if err := o.recordExit(p); err != nil {
			return err
		}
	}
	return nil
}

// release creates the release file once the target's workload has failed
// often enough.
func (o *observer) release() error {
	if o.cfg.ReleaseFile == "" || len(o.rep.Releases) > 0 {
		return nil
	}
	failed := 0
	for _, g := range o.rep.Generations {
		if g.Role == RoleWorkload && g.Account == o.cfg.Target && g.Exited != 0 && g.ExitCode != 0 && g.Crashed == 0 {
			failed++
		}
	}
	if failed < o.cfg.ReleaseAfter {
		return nil
	}
	f, err := os.OpenFile(o.cfg.ReleaseFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("release file: %w", err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	o.rep.Releases = append(o.rep.Releases, Mark{Name: "release", At: filetimeNow()})
	return nil
}

// marks timestamps each new entry of the marks directory at this scan.
func (o *observer) marks() error {
	if o.cfg.Marks == "" {
		return nil
	}
	entries, err := os.ReadDir(o.cfg.Marks)
	if err != nil {
		return fmt.Errorf("marks: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		if !markPattern.MatchString(name) || name == MarkStart || slices.ContainsFunc(o.rep.Marks, func(m Mark) bool { return m.Name == name }) {
			continue
		}
		if len(o.rep.Marks) >= maxMarks {
			return errors.New("too many marks")
		}
		o.rep.Marks = append(o.rep.Marks, Mark{Name: name, At: o.last})
	}
	return nil
}

func imagePath(h windows.Handle) (string, error) {
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf[:n]), nil
}

// tokenFactsOf reads a process's primary token: account, session,
// elevation, source and logon session.
func tokenFactsOf(h windows.Handle) (TokenFacts, error) {
	var tok windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY|windows.TOKEN_QUERY_SOURCE, &tok); err != nil {
		return TokenFacts{}, err
	}
	defer tok.Close()
	var f TokenFacts
	user, err := tok.GetTokenUser()
	if err != nil {
		return f, err
	}
	f.SID = user.User.Sid.String()
	var n uint32
	if err := windows.GetTokenInformation(tok, windows.TokenSessionId, (*byte)(unsafe.Pointer(&f.Session)), 4, &n); err != nil {
		return f, err
	}
	f.Elevated = tok.IsElevated()
	var stats tokenStatistics
	if err := windows.GetTokenInformation(tok, windows.TokenStatistics, (*byte)(unsafe.Pointer(&stats)), uint32(unsafe.Sizeof(stats)), &n); err != nil {
		return f, err
	}
	f.AuthenticationID = fmt.Sprintf("%08x:%08x", stats.AuthenticationID.HighPart, stats.AuthenticationID.LowPart)
	var source struct {
		Name [8]byte
		ID   windows.LUID
	}
	if err := windows.GetTokenInformation(tok, tokenSourceClass, (*byte)(unsafe.Pointer(&source)), uint32(unsafe.Sizeof(source)), &n); err != nil {
		return f, err
	}
	f.Source = strings.TrimRight(string(source.Name[:]), "\x00 ")
	f.LogonType, f.AuthPackage, _ = logonSession(stats.AuthenticationID)
	return f, nil
}

// isServiceProcess reports whether pid is the winunitd service's process.
func isServiceProcess(pid uint32) (bool, error) {
	m, err := mgr.Connect()
	if err != nil {
		return false, fmt.Errorf("service manager: %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService("winunitd")
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("winunitd service: %w", err)
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return false, fmt.Errorf("winunitd service: %w", err)
	}
	return st.ProcessId == pid, nil
}
