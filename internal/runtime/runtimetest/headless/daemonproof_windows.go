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
	"slices"
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
	if n, err := managersRunning(*sid); err != nil {
		facts.Errors = append(facts.Errors, NativeError{Op: "managers", Win32: win32Code(err)})
	} else {
		facts.ManagersRunning = n
	}
	if *phase == "before" {
		return writeJSONFile(*out, facts)
	}
	p := DaemonLogProof{SID: *sid, Root: *root, After: facts}
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

// runInventory is SYSTEM's inventory of the fixture's machine state. With
// --receipt it writes the baseline receipt of that name before any case
// and refuses an incomplete one; with --baseline it writes the final
// inventory against that receipt, with what remains of the fixture.
func runInventory(args []string) error {
	fs := newFlags("inventory")
	var accounts, images listFlag
	fs.Var(&accounts, "account", "")
	fs.Var(&images, "image", "")
	dataDir := fs.String("data-dir", "", "")
	linger := fs.String("linger", "", "")
	receipt := fs.String("receipt", "", "")
	baseline := fs.String("baseline", "", "")
	out := fs.String("out", "", "")
	if err := parse(fs, args); err != nil {
		return err
	}
	if err := errors.Join(absPath("out", *out), absPath("data-dir", *dataDir), absPath("linger", *linger)); err != nil {
		return err
	}
	if (*receipt == "") == (*baseline == "") {
		return usage("give either --receipt NAME or --baseline PATH")
	}
	var sids []string
	for _, a := range accounts {
		_, sid, ok := strings.Cut(a, "=")
		if !ok || !sidPattern.MatchString(sid) || sid == SystemSID || slices.Contains(sids, sid) {
			return usage("--account must be ROLE=SID, once per account")
		}
		sids = append(sids, sid)
	}
	if len(sids) == 0 {
		return usage("--account is required")
	}
	self, err := selfClaim()
	if err != nil {
		return err
	}
	if self.SID != SystemSID {
		return errors.New("inventory must run as SYSTEM")
	}
	boot, err := bootIdentity()
	if err != nil {
		return err
	}
	at := filetimeNow()
	if *receipt != "" {
		if !baselinePattern.MatchString(*receipt) {
			return usage("--receipt must name the baseline")
		}
		if _, err := os.Lstat(*out); !notFound(err) {
			return errors.New("the baseline receipt already exists; it is written once, at the baseline")
		}
		facts, resources, errs := machineFacts(*dataDir, *linger, sids)
		if len(errs) > 0 {
			return fmt.Errorf("the baseline could not be read completely: %s", nativeOps(errs))
		}
		return writeJSONFile(*out, InventoryBaseline{Name: *receipt, At: at, Boot: boot,
			Scope: InventoryScope{Accounts: sids, Resources: resources}, Facts: facts})
	}
	for _, image := range images {
		if !artifactName.MatchString(image) {
			return usage("--image must name an executable image")
		}
	}
	if err := absPath("baseline", *baseline); err != nil {
		return err
	}
	data, err := readBounded(*baseline)
	if err != nil {
		return err
	}
	p := InventoryProof{At: at, Boot: boot, BaselineSHA256: hashHex(data)}
	if err := decodeStrict(data, &p.Baseline); err != nil {
		return fmt.Errorf("baseline: %w", err)
	}
	fail := func(op string, err error) { p.Errors = append(p.Errors, NativeError{Op: op, Win32: win32Code(err)}) }
	var resources []string
	broker, err := servicePID()
	if err != nil {
		fail("service-process", err)
	}
	if procs, err := imageProcesses(images, self.PID); err != nil {
		fail("processes", err)
	} else {
		resources = append(resources, ResourceProcesses)
		for _, pr := range procs {
			if broker != 0 && pr.PID == broker {
				b := pr
				p.Broker = &b
				continue
			}
			p.Processes = append(p.Processes, pr)
		}
	}
	if p.Pipes, err = fixturePipes(); err != nil {
		fail("pipes", err)
	} else {
		resources = append(resources, ResourcePipes)
	}
	var machine []string
	var errs []NativeError
	p.Facts, machine, errs = machineFacts(*dataDir, *linger, sids)
	p.Errors = append(p.Errors, errs...)
	p.Scope = InventoryScope{Accounts: sids, Images: images, Resources: append(resources, machine...)}
	return writeJSONFile(*out, p)
}

// machineFacts reads the machine state the qualification may change, and
// which resources it read.
func machineFacts(dataDir, linger string, sids []string) (MachineFacts, []string, []NativeError) {
	var f MachineFacts
	var resources []string
	var errs []NativeError
	read := func(resource string, err error) {
		if err != nil {
			errs = append(errs, NativeError{Op: resource, Win32: win32Code(err)})
			return
		}
		resources = append(resources, resource)
	}
	var err error
	f.Service, err = serviceFacts()
	read(ResourceService, err)
	f.DataDir, err = objectFacts(dataDir, false)
	if err == nil {
		// An absent linger directory is recorded as absent.
		if f.Linger, err = objectFacts(linger, false); notFound(err) {
			f.Linger, err = nil, nil
		}
	}
	read(ResourceDataACL, err)
	f.Admission, err = objectFacts(filepath.Join(dataDir, admissionPolicyName), true)
	if notFound(err) {
		f.Admission, err = nil, nil
	}
	read(ResourceAdmission, err)
	err = nil
	for _, sid := range sids {
		if _, lerr := os.Lstat(filepath.Join(linger, sid)); lerr == nil {
			f.Grants = append(f.Grants, sid)
		} else if !notFound(lerr) {
			err = lerr
		}
	}
	slices.Sort(f.Grants)
	read(ResourceGrants, err)
	f.Tasks, err = fixtureTasks()
	read(ResourceTasks, err)
	f.FirewallRules, err = fixtureFirewallRules()
	read(ResourceFirewall, err)
	f.TemplateFiles, err = templateFiles()
	read(ResourceTemplate, err)
	return f, resources, errs
}

// admissionPolicyName is the interactive admission policy file under the
// system data root.
const admissionPolicyName = "user-admission.json"

// nativeOps names the failed operations, without paths.
func nativeOps(errs []NativeError) string {
	ops := make([]string, 0, len(errs))
	for _, e := range errs {
		ops = append(ops, fmt.Sprintf("%s (%d)", e.Op, e.Win32))
	}
	return strings.Join(ops, ", ")
}

func hashHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// servicePID is the process the service control manager names as the
// running winunitd service, or 0 when it is not running or not installed.
func servicePID() (uint32, error) {
	m, err := mgr.Connect()
	if err != nil {
		return 0, err
	}
	defer m.Disconnect()
	s, err := m.OpenService("winunitd")
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return 0, err
	}
	return st.ProcessId, nil
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

// imageProcesses lists the running processes of the named images, other
// than this one, with their owners. A process that exits while it is read
// is skipped; one whose owner cannot be read is an error, since it could
// be any account's.
func imageProcesses(images []string, self uint32) ([]InventoryProcess, error) {
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
		if e.ProcessID == self || !slices.ContainsFunc(images, func(i string) bool { return strings.EqualFold(i, name) }) {
			continue
		}
		h, oerr := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, e.ProcessID)
		if errors.Is(oerr, windows.ERROR_INVALID_PARAMETER) {
			continue // exited
		}
		if oerr != nil {
			return nil, oerr
		}
		sid, serr := processSID(h)
		_ = windows.CloseHandle(h)
		if serr != nil {
			return nil, serr
		}
		out = append(out, InventoryProcess{Image: name, SID: sid, PID: e.ProcessID})
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, err
	}
	return out, nil
}

// managersRunning counts the daemon image's processes running as sid.
func managersRunning(sid string) (int, error) {
	procs, err := imageProcesses([]string{daemonImage}, 0)
	n := 0
	for _, p := range procs {
		if p.SID == sid {
			n++
		}
	}
	return n, err
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
	output, err := quietOutput(exec.Command("schtasks.exe", "/query", "/fo", "csv", "/nh"))
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
	slices.Sort(out)
	return out, nil
}

// fixtureFirewallRules lists firewall rules named with the fixture's
// prefix. It reads every rule and filters, so an empty result means none;
// any provider or query error fails the read.
func fixtureFirewallRules() ([]string, error) {
	output, err := quietOutput(exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
		"$ErrorActionPreference = 'Stop'; Get-NetFirewallRule | Where-Object { $_.Name -like '"+fixtureName+"*' } | ForEach-Object Name"))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(output), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	slices.Sort(out)
	return out, nil
}

// quietOutput runs a query command and returns its output. A nonzero exit
// or anything written to standard error fails the query; neither the
// command's path nor its error text is reported.
func quietOutput(cmd *exec.Cmd) ([]byte, error) {
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return nil, baseOnly(err)
	}
	if stderr.Len() > 0 {
		return nil, fmt.Errorf("%s reported an error", filepath.Base(cmd.Path))
	}
	return output, nil
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
	slices.Sort(out)
	return out, err
}
