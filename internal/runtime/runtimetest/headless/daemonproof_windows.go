//go:build windows

package headless

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc/mgr"
)

// fixtureName prefixes the fixture's scheduled tasks and firewall rules.
const fixtureName = "winunitd-qual"

// objectFacts reads an object's owner and DACL and, for a file, its size
// and SHA-256.
func objectFacts(path string, file bool) (*ObjectFacts, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return nil, err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return nil, err
	}
	dsd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, err
	}
	o := &ObjectFacts{Owner: owner.String(), DACL: dsd.String()}
	if file {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		h := sha256.New()
		n, err := io.Copy(h, f)
		if err != nil {
			return nil, err
		}
		o.Size, o.SHA256 = n, hex.EncodeToString(h.Sum(nil))
	}
	return o, nil
}

// daemonLogFacts reads a manager's diagnostics under its data root: the
// daemon directory, the current log and its archive, and the current log's
// last records. An absent archive is not an error.
func daemonLogFacts(root string) DaemonLogFacts {
	f := DaemonLogFacts{At: filetimeNow()}
	fail := func(op string, err error) { f.Errors = append(f.Errors, NativeError{Op: op, Win32: win32Code(err)}) }
	dir := filepath.Join(root, daemonDirName)
	var err error
	if f.Dir, err = objectFacts(dir, false); err != nil {
		fail("daemon-directory", err)
	}
	current := filepath.Join(dir, daemonLogName)
	if f.Current, err = objectFacts(current, true); err != nil {
		fail("current-log", err)
	}
	if f.Archive, err = objectFacts(current+".1", true); err != nil {
		f.Archive = nil
		if !notFound(err) {
			fail("archive", err)
		}
	}
	if f.Current != nil {
		if f.Tail, err = logTail(current); err != nil {
			fail("current-log-tail", err)
		}
	}
	return f
}

// runProbeDaemonLog records a manager's diagnostics. Before the declared
// intervention it writes the facts; after it, with --before, it writes the
// proof that joins both; without --before, the after facts alone.
func runProbeDaemonLog(args []string) error {
	fs := newFlags("probe-daemon-log")
	root := fs.String("root", "", "")
	sid := fs.String("sid", "", "")
	phase := fs.String("phase", "", "")
	before := fs.String("before", "", "")
	out := fs.String("out", "", "")
	if err := parse(fs, args); err != nil {
		return err
	}
	if err := errors.Join(absPath("root", *root), absPath("out", *out)); err != nil {
		return err
	}
	if !sidPattern.MatchString(*sid) || *phase != "before" && *phase != "after" {
		return usage("--sid must be the manager's SID and --phase before or after")
	}
	facts := daemonLogFacts(*root)
	if *phase == "before" {
		return writeJSONFile(*out, facts)
	}
	p := DaemonLogProof{SID: *sid, After: facts}
	if *before != "" {
		if err := absPath("before", *before); err != nil {
			return err
		}
		data, err := readBounded(*before)
		if err != nil {
			return err
		}
		var b DaemonLogFacts
		if err := decodeStrict(data, &b); err != nil {
			return fmt.Errorf("before: %w", err)
		}
		p.Before = &b
	}
	return writeJSONFile(*out, p)
}

// daemonDenial reads the account's own daemon log and tries the peer's and
// the system manager's daemon directories and logs.
func daemonDenial(c *ProbeConfig) []PathResult {
	dirResult := func(probe, dir string) PathResult {
		_, err := os.ReadDir(dir)
		r := PathResult{Probe: probe, OK: err == nil}
		if err != nil {
			r.Win32 = win32Code(err)
		}
		return r
	}
	logOf := func(root string) string { return filepath.Join(root, daemonDirName, daemonLogName) }
	return []PathResult{
		pathResult("own-log", readFileOp("read", logOf(c.OwnDaemon))),
		dirResult("peer-directory", filepath.Join(c.PeerDaemon, daemonDirName)),
		pathResult("peer-log", readFileOp("read", logOf(c.PeerDaemon))),
		dirResult("system-directory", filepath.Join(c.SystemDaemon, daemonDirName)),
		pathResult("system-log", readFileOp("read", logOf(c.SystemDaemon))),
	}
}

// runInventory is SYSTEM's inventory. With --service-only it records the
// winunitd service configuration at the baseline; otherwise it lists what
// the qualification left behind and joins the baseline's service record.
func runInventory(args []string) error {
	fs := newFlags("inventory")
	var accounts listFlag
	fs.Var(&accounts, "account", "")
	linger := fs.String("linger", "", "")
	baseline := fs.String("baseline", "", "")
	baselineService := fs.String("baseline-service", "", "")
	serviceOnly := fs.Bool("service-only", false, "")
	out := fs.String("out", "", "")
	if err := parse(fs, args); err != nil {
		return err
	}
	if err := absPath("out", *out); err != nil {
		return err
	}
	self, err := selfClaim()
	if err != nil {
		return err
	}
	if self.SID != SystemSID {
		return errors.New("inventory must run as SYSTEM")
	}
	var p InventoryProof
	fail := func(op string, err error) { p.Errors = append(p.Errors, NativeError{Op: op, Win32: win32Code(err)}) }
	if p.Service, err = serviceFacts(); err != nil {
		fail("service", err)
	}
	if *serviceOnly {
		if len(p.Errors) > 0 {
			return errors.New("the service configuration could not be read")
		}
		return writeJSONFile(*out, p.Service)
	}
	if !baselinePattern.MatchString(*baseline) {
		return usage("--baseline must name the baseline")
	}
	if err := errors.Join(absPath("linger", *linger), absPath("baseline-service", *baselineService)); err != nil {
		return err
	}
	sids := map[string]bool{}
	for _, a := range accounts {
		_, sid, ok := strings.Cut(a, "=")
		if !ok || !sidPattern.MatchString(sid) || sid == SystemSID {
			return usage("--account must be ROLE=SID")
		}
		sids[sid] = true
	}
	if len(sids) == 0 {
		return usage("--account is required")
	}
	p.Baseline, p.At = *baseline, filetimeNow()
	data, err := readBounded(*baselineService)
	if err == nil {
		err = decodeStrict(data, &p.BaselineService)
	}
	if err != nil {
		fail("baseline-service", err)
	}
	if p.Processes, err = ownedProcesses(self.PID); err != nil {
		fail("processes", err)
	}
	if p.Pipes, err = fixturePipes(); err != nil {
		fail("pipes", err)
	}
	if p.Tasks, err = fixtureTasks(); err != nil {
		fail("tasks", err)
	}
	if p.FirewallRules, err = fixtureFirewallRules(); err != nil {
		fail("firewall", err)
	}
	for sid := range sids {
		if _, err := os.Lstat(filepath.Join(*linger, sid)); err == nil {
			p.Grants = append(p.Grants, sid)
		} else if !notFound(err) {
			fail("linger", err)
		}
	}
	if p.TemplateFiles, err = templateFiles(); err != nil {
		fail("template", err)
	}
	return writeJSONFile(*out, p)
}

// serviceFacts is the winunitd service's start type, binary path hash and
// recovery actions.
func serviceFacts() (ServiceFacts, error) {
	m, err := mgr.Connect()
	if err != nil {
		return ServiceFacts{}, err
	}
	defer m.Disconnect()
	s, err := m.OpenService("winunitd")
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return ServiceFacts{}, nil
	}
	if err != nil {
		return ServiceFacts{}, err
	}
	defer s.Close()
	cfg, err := s.Config()
	if err != nil {
		return ServiceFacts{}, err
	}
	actions, err := s.RecoveryActions()
	if err != nil {
		return ServiceFacts{}, err
	}
	reset, err := s.ResetPeriod()
	if err != nil {
		return ServiceFacts{}, err
	}
	sum := sha256.Sum256([]byte(strings.ToLower(cfg.BinaryPathName)))
	var recovery []string
	for _, a := range actions {
		recovery = append(recovery, fmt.Sprintf("%d/%d", a.Type, a.Delay.Milliseconds()))
	}
	return ServiceFacts{Installed: true, StartType: cfg.StartType, BinaryPath: hex.EncodeToString(sum[:]),
		Recovery: strings.Join(recovery, ";") + fmt.Sprintf(";reset/%d", reset)}, nil
}

// ownedProcesses lists processes of the daemon image not running as SYSTEM
// (user managers) and of the workload image (workloads, probes, clients),
// other than this one.
func ownedProcesses(self uint32) ([]InventoryProcess, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	var out []InventoryProcess
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		name := windows.UTF16ToString(e.ExeFile[:])
		daemon, work := strings.EqualFold(name, daemonImage), strings.EqualFold(name, workloadImage)
		if e.ProcessID == self || !daemon && !work {
			continue
		}
		sid := ""
		if h, oerr := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, e.ProcessID); oerr == nil {
			sid, _ = processSID(h)
			_ = windows.CloseHandle(h)
		}
		if daemon && sid == SystemSID {
			continue // the broker
		}
		out = append(out, InventoryProcess{Image: name, SID: sid, PID: e.ProcessID})
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, err
	}
	return out, nil
}

// fixturePipes lists served pipes in the fixture's namespace.
func fixturePipes() ([]string, error) {
	pattern, err := windows.UTF16PtrFromString(`\\.\pipe\*`)
	if err != nil {
		return nil, err
	}
	var data windows.Win32finddata
	h, err := windows.FindFirstFile(pattern, &data)
	if err != nil {
		return nil, err
	}
	defer windows.FindClose(h)
	var out []string
	for {
		if name := windows.UTF16ToString(data.FileName[:]); strings.HasPrefix(strings.ToLower(name), fixtureName+`\`) {
			out = append(out, name)
		}
		if err := windows.FindNextFile(h, &data); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				return out, nil
			}
			return nil, err
		}
	}
}

// fixtureTasks lists scheduled tasks in the fixture's folder.
func fixtureTasks() ([]string, error) {
	output, err := exec.Command("schtasks.exe", "/query", "/fo", "csv", "/nh").Output()
	if err != nil {
		return nil, err
	}
	rows, err := csv.NewReader(bytes.NewReader(output)).ReadAll()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, row := range rows {
		if len(row) > 0 && strings.HasPrefix(strings.ToLower(row[0]), `\`+fixtureName) {
			out = append(out, row[0])
		}
	}
	return out, nil
}

// fixtureFirewallRules lists firewall rules named with the fixture's
// prefix.
func fixtureFirewallRules() ([]string, error) {
	output, err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
		"Get-NetFirewallRule -Name '"+fixtureName+"*' -ErrorAction SilentlyContinue | ForEach-Object Name").Output()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(output), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out, nil
}

// templateFiles lists WinUnit files left in the Default profile template.
func templateFiles() ([]string, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, profileListKey, registry.QUERY_VALUE)
	if err != nil {
		return nil, err
	}
	def, _, err := k.GetStringValue("Default")
	_ = k.Close()
	if err != nil {
		return nil, err
	}
	if def, err = registry.ExpandString(def); err != nil {
		return nil, err
	}
	root := filepath.Join(def, "AppData", "Local", "winunitd")
	var out []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(root, path)
			out = append(out, rel)
		}
		return nil
	})
	if notFound(err) {
		return nil, nil
	}
	return out, err
}
