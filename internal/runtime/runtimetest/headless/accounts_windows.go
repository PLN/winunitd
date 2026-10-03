//go:build windows

package headless

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unsafe"

	"github.com/PLN/winunitd/internal/protocol"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var procLsaEnumerateLogonSessions = secur32.NewProc("LsaEnumerateLogonSessions")

const profileListKey = `SOFTWARE\Microsoft\Windows NT\CurrentVersion\ProfileList`

// logonSessionFull is SECURITY_LOGON_SESSION_DATA up to its logon time.
type logonSessionFull struct {
	Size                  uint32
	LogonID               windows.LUID
	UserName              windows.NTUnicodeString
	LogonDomain           windows.NTUnicodeString
	AuthenticationPackage windows.NTUnicodeString
	LogonType             uint32
	Session               uint32
	Sid                   *windows.SID
	LogonTime             int64
}

// logonSessions lists every logon session's ID, account, type and logon
// time. A session LSA refuses to describe is skipped; the enumeration
// itself failing is an error.
func logonSessions() ([]LogonFact, error) {
	var count uint32
	var list *windows.LUID
	if r, _, _ := procLsaEnumerateLogonSessions.Call(uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&list))); r != 0 {
		return nil, fmt.Errorf("enumerate logon sessions: %w", windows.NTStatus(r).Errno())
	}
	defer procLsaFreeReturnBuffer.Call(uintptr(unsafe.Pointer(list)))
	var out []LogonFact
	for _, id := range unsafe.Slice(list, count) {
		var data *logonSessionFull
		r, _, _ := procLsaGetLogonSessionData.Call(uintptr(unsafe.Pointer(&id)), uintptr(unsafe.Pointer(&data)))
		switch {
		case r == statusNoSuchLogonSession:
			// It ended between the enumeration and the query.
			continue
		case r != 0 || data == nil:
			return nil, fmt.Errorf("describe a logon session: %w", windows.NTStatus(r).Errno())
		}
		if data.Sid != nil {
			out = append(out, LogonFact{ID: fmt.Sprintf("%08x:%08x", id.HighPart, id.LowPart), SID: data.Sid.String(), Type: data.LogonType,
				LogonTime: uint64(data.LogonTime), Source: "sample"})
		}
		procLsaFreeReturnBuffer.Call(uintptr(unsafe.Pointer(data)))
	}
	return out, nil
}

// statusNoSuchLogonSession is STATUS_NO_SUCH_LOGON_SESSION.
const statusNoSuchLogonSession = 0xC000005F

// interactiveUsers lists the sessions other than zero that have a user,
// with that user's SID.
func interactiveUsers() ([]SessionUser, error) {
	var info *windows.WTS_SESSION_INFO
	var n uint32
	if err := windows.WTSEnumerateSessions(0, 0, 1, &info, &n); err != nil {
		return nil, fmt.Errorf("enumerate sessions: %w", err)
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(info)))
	var out []SessionUser
	for _, s := range unsafe.Slice(info, n) {
		if s.SessionID == 0 {
			continue
		}
		var tok windows.Token
		if err := windows.WTSQueryUserToken(s.SessionID, &tok); errors.Is(err, windows.ERROR_NO_TOKEN) {
			continue // positively no user in this session
		} else if err != nil {
			return nil, fmt.Errorf("session user: %w", err)
		}
		user, err := tok.GetTokenUser()
		_ = tok.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, SessionUser{Session: s.SessionID, SID: user.User.Sid.String(), State: s.State})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Session < out[j].Session })
	return out, nil
}

func notFound(err error) bool {
	return errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND)
}

// profileFacts reads an account's profile registration, the creation time
// of its directory and whether its hive is loaded, and returns the profile
// directory when it is registered.
func profileFacts(sid string) (ProfileFacts, string) {
	var f ProfileFacts
	fail := func(op string, err error) { f.Errors = append(f.Errors, NativeError{Op: op, Win32: win32Code(err)}) }
	dir := ""
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, profileListKey+`\`+sid, registry.QUERY_VALUE)
	switch {
	case notFound(err):
	case err != nil:
		fail("profile-list", err)
	default:
		f.Registered = true
		path, _, err := k.GetStringValue("ProfileImagePath")
		if err == nil {
			path, err = registry.ExpandString(path)
		}
		_ = k.Close()
		if err != nil {
			fail("profile-path", err)
			break
		}
		dir, f.Path = path, path
		if created, err := directoryCreated(path); err == nil {
			f.DirectoryCreated = created
		} else if !notFound(err) {
			fail("profile-directory", err)
		}
	}
	hive, err := registry.OpenKey(registry.USERS, sid, registry.QUERY_VALUE)
	switch {
	case notFound(err):
	case err != nil:
		fail("hive", err)
	default:
		f.HiveLoaded = true
		_ = hive.Close()
	}
	return f, dir
}

func directoryCreated(path string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var data windows.Win32FileAttributeData
	if err := windows.GetFileAttributesEx(p, windows.GetFileExInfoStandard, (*byte)(unsafe.Pointer(&data))); err != nil {
		return 0, err
	}
	if data.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return 0, errors.New("the profile path is not a directory")
	}
	return uint64(data.CreationTime.HighDateTime)<<32 | uint64(data.CreationTime.LowDateTime), nil
}

// workloadProgress reads the workload's progress record from its state
// root under the profile directory.
func workloadProgress(profileDir string) (Progress, bool) {
	data, err := readBounded(filepath.Join(profileDir, "AppData", "Local", "winunitd-qual", "headless-workload", "progress.json"))
	if err != nil {
		return Progress{}, false
	}
	var p Progress
	if decodeStrict(data, &p) != nil {
		return Progress{}, false
	}
	return p, true
}

// runProbeFirstUse is SYSTEM's check, on the sealed baseline, that the
// account has no profile registration, profile directory, loaded hive or
// logon session. It must run immediately before the cold boot.
func runProbeFirstUse(args []string) error {
	fs := newFlags("probe-first-use")
	sid := fs.String("sid", "", "")
	receipt := fs.String("baseline-receipt", "", "")
	out := fs.String("out", "", "")
	if err := parse(fs, args); err != nil {
		return err
	}
	if !sidPattern.MatchString(*sid) || *sid == SystemSID {
		return usage("--sid must be an account SID")
	}
	if err := errors.Join(absPath("out", *out), absPath("baseline-receipt", *receipt)); err != nil {
		return err
	}
	// The baseline receipt this check follows: its name and the hash of
	// its bytes, which the final inventory must present again.
	data, err := readBounded(*receipt)
	if err != nil {
		return err
	}
	var b InventoryBaseline
	if err := decodeStrict(data, &b); err != nil {
		return fmt.Errorf("baseline receipt: %w", err)
	}
	if !baselinePattern.MatchString(b.Name) {
		return errors.New("the baseline receipt names no baseline")
	}
	p := FirstUseProof{SID: *sid, Baseline: b.Name, BaselineSHA256: hashHex(data)}
	if p.Boot, err = bootIdentity(); err != nil {
		return err
	}
	facts, _ := profileFacts(*sid)
	p.ProfileList, p.HiveLoaded, p.Errors = facts.Registered, facts.HiveLoaded, facts.Errors
	// An unregistered account's profile would be created under the
	// profiles directory with its account name.
	if dir, err := unregisteredProfileDir(*sid); err != nil {
		p.Errors = append(p.Errors, NativeError{Op: "profile-directory", Win32: win32Code(err)})
	} else if _, err := os.Stat(dir); err == nil {
		p.ProfileDirectory = true
	} else if !notFound(err) {
		p.Errors = append(p.Errors, NativeError{Op: "profile-directory", Win32: win32Code(err)})
	}
	sessions, err := logonSessions()
	if err != nil {
		return err
	}
	for _, s := range sessions {
		if s.SID == *sid {
			p.LogonSessions++
		}
	}
	p.At = filetimeNow()
	return writeJSONFile(*out, p)
}

func unregisteredProfileDir(sid string) (string, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, profileListKey, registry.QUERY_VALUE)
	if err != nil {
		return "", err
	}
	root, _, err := k.GetStringValue("ProfilesDirectory")
	_ = k.Close()
	if err != nil {
		return "", err
	}
	if root, err = registry.ExpandString(root); err != nil {
		return "", err
	}
	s, err := windows.StringToSid(sid)
	if err != nil {
		return "", err
	}
	name, _, _, err := s.LookupAccount("")
	if err != nil {
		return "", err
	}
	return filepath.Join(root, name), nil
}

// sampledPasswordLogon reports a logon type that is password-bearing in an
// LSA sample. A batch logon is left to the audited history, which names the
// logon process and so separates the product's S4U batch logons.
func sampledPasswordLogon(t uint32) bool {
	return t != logonBatch && slices.Contains(passwordLogonTypes, t)
}

// EndpointReport is SYSTEM's view of one pipe's live server.
type EndpointReport struct {
	Client string         `json:"client"`
	Pipe   string         `json:"pipe"`
	Server EndpointServer `json:"server"`
}

// runProbeEndpoint connects to a pipe as SYSTEM and identifies the process
// serving it, so the account's denials are known to concern the genuine
// live endpoint. The driver runs it before and after those denials.
func runProbeEndpoint(args []string) error {
	fs := newFlags("probe-endpoint")
	pipe := fs.String("pipe", "", "")
	client := fs.String("client", "", "")
	out := fs.String("out", "", "")
	if err := parse(fs, args); err != nil {
		return err
	}
	if !strings.HasPrefix(*pipe, `\\.\pipe\`) || !controlPattern.MatchString(*client) {
		return usage("--pipe must be a local pipe and --client a role")
	}
	if err := absPath("out", *out); err != nil {
		return err
	}
	self, err := selfClaim()
	if err != nil {
		return err
	}
	if self.SID != SystemSID {
		return errors.New("probe-endpoint must run as SYSTEM")
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	c, err := protocol.DialPipe(ctx, *pipe)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer c.Close()
	h, ok := pipeHandle(c)
	if !ok {
		return errors.New("no pipe handle")
	}
	var rep EndpointReport
	rep.Client, rep.Pipe = *client, *pipe
	if err := windows.GetNamedPipeServerProcessId(h, &rep.Server.PID); err != nil {
		return fmt.Errorf("server process: %w", err)
	}
	proc, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, rep.Server.PID)
	if err != nil {
		return fmt.Errorf("open server: %w", err)
	}
	defer windows.CloseHandle(proc)
	if rep.Server.Created, err = creationTime(proc); err != nil {
		return err
	}
	if rep.Server.SID, err = processSID(proc); err != nil {
		return err
	}
	image, err := imagePath(proc)
	if err != nil {
		return err
	}
	rep.Server.Image = filepath.Base(image)
	return writeJSONFile(*out, rep)
}

// runTestReceipt runs one admitted native test binary and writes its
// receipt. The binary runs as this recorder's child, in its context and in
// a kill-on-close job, bounded in time and output, with its output turned
// into events by test2json. The receipt keeps this recorder's and the test
// process's incarnations and tokens, the test process's exit code, every
// test and package action and, with --subject, the subject report the test
// wrote during this run; an older report is removed first.
func runTestReceipt(args []string) error {
	fs := newFlags("test-receipt")
	events := fs.String("events", "", "")
	artifact := fs.String("artifact", "", "")
	binary := fs.String("binary", "", "")
	runner := fs.String("runner", "", "")
	subject := fs.String("subject", "", "")
	run := fs.String("run", "", "")
	test2json := fs.String("test2json", "", "")
	timeout := fs.Duration("timeout", 25*time.Minute, "")
	out := fs.String("out", "", "")
	if err := parse(fs, args); err != nil {
		return err
	}
	if err := errors.Join(absPath("test2json", *test2json), absPath("binary", *binary), absPath("events", *events), absPath("out", *out)); err != nil {
		return err
	}
	if !artifactName.MatchString(*artifact) || !executionPattern.MatchString(*runner) || *run == "" || *timeout <= 0 || *timeout > 2*time.Hour {
		return usage("--artifact must be an artifact name, --runner an ID, --run the tests and --timeout at most 2h")
	}
	if _, err := regexp.Compile(*run); err != nil {
		return usage("--run must be a test pattern")
	}
	if *subject != "" {
		if err := absPath("subject", *subject); err != nil {
			return err
		}
		if err := os.Remove(*subject); err != nil && !notFound(err) {
			return baseOnly(err)
		}
	}
	self, err := selfClaim()
	if err != nil {
		return err
	}
	p := TestRunProof{Artifact: *artifact, Runner: RunnerFacts{ID: *runner, PID: self.PID, Created: self.Created}}
	if p.Runner.Token, err = tokenFactsOf(windows.CurrentProcess()); err != nil {
		return err
	}
	if p.SHA256, err = FileSHA256(*binary); err != nil {
		return err
	}
	output, err := runNativeTest(&p, *binary, *test2json, *run, *subject, *timeout)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*events, output, 0o600); err != nil {
		return baseOnly(err)
	}
	if p.Events, err = ParseTestEvents(bytes.NewReader(output)); err != nil {
		return err
	}
	if *subject != "" {
		var s SubjectReport
		data, err := readBounded(*subject)
		if err == nil {
			err = decodeStrict(data, &s)
		}
		if err != nil {
			p.Native = append(p.Native, NativeError{Op: "subject", Win32: win32Code(err)})
		} else {
			p.Subject = &s
		}
	}
	if err := writeJSONFile(*out, p); err != nil {
		return err
	}
	switch {
	case p.ExitCode != 0:
		return fmt.Errorf("the test process exited with %d", p.ExitCode)
	case len(p.Native) > 0:
		return errors.New("the test wrote no subject report")
	}
	return nil
}

// runNativeTest runs the test binary under custody, piping its output
// through test2json, and records the test process and its exit code. Any
// process the run leaves behind is terminated before it returns.
func runNativeTest(p *TestRunProof, binary, test2json, run, subject string, bound time.Duration) (output []byte, err error) {
	c, err := newCustody()
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, c.end(30*time.Second)) }()
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	defer w.Close()
	conv := exec.Command(test2json, "-t", "-p", strings.TrimSuffix(filepath.Base(binary), ".test.exe"))
	conv.Stdin = r
	var buf boundedBuffer
	conv.Stdout = &buf
	test := exec.Command(binary, "-test.v=test2json", "-test.run", run, "-test.count=1")
	test.Stdout, test.Stderr = w, w
	test.Env = slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(strings.ToUpper(kv), qualSubjectEnv+"=") })
	if subject != "" {
		test.Env = append(test.Env, qualSubjectEnv+"="+subject)
	}
	if err := c.start(conv); err != nil {
		return nil, fmt.Errorf("start test2json: %w", err)
	}
	convDone := make(chan error, 1)
	go func() { convDone <- conv.Wait() }()
	if err := c.start(test); err != nil {
		c.terminate()
		<-convDone
		return nil, fmt.Errorf("start the test: %w", err)
	}
	// The children hold their ends of the pipe; the output ends when the
	// test process and anything it handed the pipe to are gone.
	_ = r.Close()
	_ = w.Close()
	timer := time.AfterFunc(bound, c.terminate)
	defer timer.Stop()
	p.Owner.PID = uint32(test.Process.Pid)
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, p.Owner.PID)
	if err == nil {
		p.Owner.Created, err = creationTime(h)
		if err == nil {
			p.Owner.Token, err = tokenFactsOf(h)
		}
		_ = windows.CloseHandle(h)
	}
	testErr := test.Wait()
	if err != nil {
		return nil, fmt.Errorf("identify the test process: %w", err)
	}
	var convErr error
	select {
	case convErr = <-convDone:
	case <-time.After(30 * time.Second):
		c.terminate()
		<-convDone
		convErr = errors.New("the test output did not end with the test process")
	}
	if !timer.Stop() {
		return nil, errors.New("the test run exceeded its time bound")
	}
	var exit *exec.ExitError
	switch {
	case testErr == nil:
	case errors.As(testErr, &exit):
		p.ExitCode = exit.ExitCode()
	default:
		return nil, baseOnly(testErr)
	}
	switch {
	case convErr != nil:
		return nil, fmt.Errorf("test2json: %w", baseOnly(convErr))
	case buf.truncated:
		return nil, errors.New("the test output exceeded its bound")
	}
	return buf.Bytes(), nil
}

// qualSubjectEnv names the file a native test writes its subject report
// to.
const qualSubjectEnv = "WINUNITD_QUAL_SUBJECT_OUT"

// runUnitStatus reads a unit's status from the account's own manager with
// winctl --user snapshot. It runs inside the unit, as the account.
func runUnitStatus(args []string) error {
	fs := newFlags("unit-status")
	config := fs.String("config", "", "")
	out := fs.String("out", "", "")
	if err := parse(fs, args); err != nil {
		return err
	}
	if err := errors.Join(absPath("config", *config), absPath("out", *out)); err != nil {
		return err
	}
	cfg, err := LoadProbeConfig(*config)
	if err != nil {
		return err
	}
	if cfg.Winctl == "" || cfg.StatusUnit == "" {
		return usage("the configuration names no winctl or unit")
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	data, err := exec.CommandContext(ctx, cfg.Winctl, "--user", "snapshot").Output()
	if err != nil {
		return fmt.Errorf("winctl snapshot: %w", baseOnly(err))
	}
	var snap protocol.SnapshotResult
	if err := json.Unmarshal(data, &snap); err != nil {
		return fmt.Errorf("winctl snapshot: %w", err)
	}
	p, err := StatusFromSnapshot(&snap, cfg.StatusUnit, filetimeNow())
	if err != nil {
		return err
	}
	return writeJSONFile(*out, p)
}

// runStartUnit starts the account's outside unit through its own manager
// with winctl --user, as the account. The unit runs H15's outside-unit
// client, which writes its own result.
func runStartUnit(args []string) error {
	fs := newFlags("start-unit")
	config := fs.String("config", "", "")
	out := fs.String("out", "", "")
	if err := parse(fs, args); err != nil {
		return err
	}
	if err := errors.Join(absPath("config", *config), absPath("out", *out)); err != nil {
		return err
	}
	cfg, err := LoadProbeConfig(*config)
	if err != nil {
		return err
	}
	if cfg.Winctl == "" || !strings.HasSuffix(cfg.OutsideUnit, ".service") {
		return usage("the configuration names no winctl or outside unit")
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	if err := exec.CommandContext(ctx, cfg.Winctl, "--user", "start", cfg.OutsideUnit).Run(); err != nil {
		return fmt.Errorf("start the outside unit: %w", baseOnly(err))
	}
	return writeJSONFile(*out, map[string]bool{"started": true})
}
