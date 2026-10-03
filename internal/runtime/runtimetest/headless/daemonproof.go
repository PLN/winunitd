package headless

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The product's daemon-log layout under a manager's data root.
const (
	daemonDirName  = "daemon"
	daemonLogName  = "daemon.log"
	daemonTailSize = 16 << 10
)

// Daemon-log checks a daemon-log proof can be held to.
const (
	// CheckRotation: a padded stopped log is rotated when its manager
	// starts again, and the diagnostics stay protected and fresh.
	CheckRotation = "rotation"
	// CheckRepair: a legacy directory owned by the account with an open
	// DACL is repaired by the manager's first start, with no manual grant.
	CheckRepair = "repair"
	// CheckProtection: the system manager's diagnostics keep the machine-only
	// DACL.
	CheckProtection = "protection"
)

// filetimeEpoch is the Unix epoch as a FILETIME.
const filetimeEpoch = 116444736000000000

// RotationBytes is the product's daemon-log rotation size.
const RotationBytes = 256 << 10

// The daemon-log record that opens a log.
const daemonOpenCode = "daemon.open"

// ObjectFacts is one daemon-log object's security and, for files, size and
// content hash.
type ObjectFacts struct {
	Owner  string `json:"owner"`
	DACL   string `json:"dacl"`
	Size   int64  `json:"size,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

// LogRecord is one daemon-log record's code and time, as a FILETIME.
type LogRecord struct {
	Code string `json:"code"`
	At   uint64 `json:"at"`
}

// DaemonLogFacts is a manager's diagnostics directory at At: the directory,
// the current log, its archive and the current log's last records.
type DaemonLogFacts struct {
	At      uint64        `json:"at"`
	Dir     *ObjectFacts  `json:"dir,omitempty"`
	Current *ObjectFacts  `json:"current,omitempty"`
	Archive *ObjectFacts  `json:"archive,omitempty"`
	Tail    []LogRecord   `json:"tail,omitempty"`
	Errors  []NativeError `json:"errors,omitempty"`
}

// DaemonLogProof is the diagnostics of the manager that runs as SID, before
// the declared intervention (padding the stopped log, or preparing a legacy
// directory) and after the manager started again.
type DaemonLogProof struct {
	SID    string          `json:"sid"`
	Before *DaemonLogFacts `json:"before,omitempty"`
	After  DaemonLogFacts  `json:"after"`
}

var aceToken = regexp.MustCompile(`\([^()]*\)`)

// protectedDACL reports whether an SDDL DACL is the product's protected
// daemon-log DACL for a manager running as sid: full control for SYSTEM,
// Administrators and, unless sid is SYSTEM, the manager's own account, and
// nothing else.
func protectedDACL(dacl, sid string) bool {
	if !strings.HasPrefix(dacl, "D:") {
		return false
	}
	flags := dacl[2:]
	if i := strings.IndexByte(flags, '('); i >= 0 {
		flags = flags[:i]
	}
	if !strings.Contains(flags, "P") {
		return false
	}
	want := []string{"(A;;FA;;;BA)", "(A;;FA;;;SY)"}
	if sid != SystemSID {
		want = append(want, "(A;;FA;;;"+sid+")")
	}
	got := aceToken.FindAllString(dacl, -1)
	slices.Sort(want)
	slices.Sort(got)
	return slices.Equal(got, want)
}

func daemonOwnerAllowed(owner, sid string) bool {
	return owner == sid || owner == SystemSID || owner == builtinAdministrators
}

// CheckDaemonLog validates a daemon-log proof for the manager running as
// sid. Rotation and repair also need the observer to have held a manager of
// the account, under the mode's token class, started after the
// intervention and still running at the end.
func CheckDaemonLog(p *DaemonLogProof, check, sid, account, mode string, rep *ObserverReport) []string {
	if p == nil {
		return []string{"no daemon-log proof"}
	}
	var problems []string
	add := func(s string) { problems = append(problems, s) }
	if p.SID != sid {
		add("the daemon-log proof names another account")
	}
	if len(p.After.Errors) > 0 || p.Before != nil && len(p.Before.Errors) > 0 {
		add("a daemon-log query failed")
	}
	a := p.After
	protected := func(name string, o *ObjectFacts) {
		switch {
		case o == nil:
			add("the " + name + " is missing")
		case !daemonOwnerAllowed(o.Owner, sid):
			add("the " + name + " has an unexpected owner")
		case !protectedDACL(o.DACL, sid):
			add("the " + name + " is not protected for its manager only")
		}
	}
	opened := func(after uint64) bool {
		return slices.ContainsFunc(a.Tail, func(r LogRecord) bool { return r.Code == daemonOpenCode && r.At > after })
	}
	switch check {
	case CheckProtection:
		protected("daemon directory", a.Dir)
		protected("current log", a.Current)
		if a.Archive != nil {
			protected("archive", a.Archive)
		}
		if !opened(0) {
			add("the log has no daemon.open record")
		}
		return problems
	case CheckRotation, CheckRepair:
	default:
		return append(problems, "unknown daemon-log check "+check)
	}
	b := p.Before
	if b == nil || b.At == 0 {
		return append(problems, "no daemon-log facts before the intervention")
	}
	if check == CheckRotation {
		switch {
		case b.Current == nil || b.Current.Size < RotationBytes || b.Current.SHA256 == "":
			add("the stopped log was not padded past the rotation size")
		case a.Archive == nil || a.Archive.SHA256 != b.Current.SHA256 || a.Archive.Size != b.Current.Size:
			add("the padded log was not rotated to the archive unchanged")
		case a.Current == nil || a.Current.Size >= RotationBytes:
			add("no fresh current log after the rotation")
		}
		protected("archive", a.Archive)
	} else if b.Dir == nil || b.Dir.Owner != sid || protectedDACL(b.Dir.DACL, sid) {
		add("the directory was not a legacy account-owned directory before the first start")
	}
	protected("daemon directory", a.Dir)
	protected("current log", a.Current)
	if !opened(b.At) {
		add("no daemon.open record after the intervention")
	}
	if rep == nil {
		return append(problems, "no observer report of the manager's start")
	}
	started := slices.ContainsFunc(rep.Generations, func(g Generation) bool {
		return g.Role == RoleManager && g.Account == account && g.Created > b.At && g.Exited == 0 && ClassifyToken(g.Token) == tokenClass(mode)
	})
	if !started {
		add("the observer held no manager of the account started after the intervention and still running")
	}
	return problems
}

// SessionToken reports whether a probed token is the account's own token
// in an interactive session: not S4U, not elevated, and of the mode's
// class (a standard session token or an administrator's filtered one).
func SessionToken(tp *TokenProbe, sid, mode string) bool {
	if tp == nil || tp.SID != sid {
		return false
	}
	f := TokenFacts{SID: tp.SID, Session: tp.Session, Elevated: tp.Elevated, Source: tp.Source, LogonType: tp.LogonType,
		ElevationType: tp.ElevationType}
	class := ClassifyToken(f)
	return class != SourceS4U && class == tokenClass(mode)
}

// InventoryProof is SYSTEM's final inventory before the snapshot revert,
// compared with the one taken at the baseline: owned processes, fixture
// pipes, scheduled tasks and firewall rules, linger grants of the
// qualification accounts, fixture files left in the Default profile
// template, and the winunitd service configuration.
type InventoryProof struct {
	Baseline      string             `json:"baseline"`
	At            uint64             `json:"at"`
	Processes     []InventoryProcess `json:"processes,omitempty"`
	Pipes         []string           `json:"pipes,omitempty"`
	Tasks         []string           `json:"tasks,omitempty"`
	FirewallRules []string           `json:"firewallRules,omitempty"`
	Grants        []string           `json:"grants,omitempty"`
	TemplateFiles []string           `json:"templateFiles,omitempty"`
	Service       ServiceFacts       `json:"service"`
	// BaselineService is the same role's record of the service at the
	// baseline, before any case ran.
	BaselineService ServiceFacts  `json:"baselineService"`
	Errors          []NativeError `json:"errors,omitempty"`
}

// InventoryProcess is one process of the installed images other than the
// broker.
type InventoryProcess struct {
	Image string `json:"image"`
	SID   string `json:"sid"`
	PID   uint32 `json:"pid"`
}

// ServiceFacts is the winunitd service's configuration: whether it is
// installed, its start type, a hash of its binary path and its recovery
// actions.
type ServiceFacts struct {
	Installed  bool   `json:"installed"`
	StartType  uint32 `json:"startType,omitempty"`
	BinaryPath string `json:"binaryPathSha256,omitempty"`
	Recovery   string `json:"recovery,omitempty"`
}

// CheckInventory requires nothing owned to remain and the service
// configuration to equal the baseline's.
func CheckInventory(p *InventoryProof) []string {
	if p == nil {
		return []string{"no final inventory"}
	}
	var problems []string
	add := func(n int, what string) {
		if n > 0 {
			problems = append(problems, what+" remain")
		}
	}
	if !baselinePattern.MatchString(p.Baseline) || p.At == 0 {
		problems = append(problems, "the inventory names no baseline")
	}
	if len(p.Errors) > 0 {
		problems = append(problems, "an inventory query failed")
	}
	add(len(p.Processes), "owned processes")
	add(len(p.Pipes), "fixture pipes")
	add(len(p.Tasks), "fixture scheduled tasks")
	add(len(p.FirewallRules), "fixture firewall rules")
	add(len(p.Grants), "qualification linger grants")
	add(len(p.TemplateFiles), "fixture files in the Default profile template")
	if p.Service != p.BaselineService || !p.Service.Installed && p.BaselineService.Installed {
		problems = append(problems, "the winunitd service configuration differs from the baseline")
	}
	return problems
}

// logTail reads the codes and times of the last records of a daemon log.
func logTail(path string) ([]LogRecord, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if off := st.Size() - daemonTailSize; off > 0 {
		if _, err := file.Seek(off, io.SeekStart); err != nil {
			return nil, err
		}
	}
	data, err := io.ReadAll(io.LimitReader(file, daemonTailSize))
	if err != nil {
		return nil, err
	}
	var out []LogRecord
	for _, line := range bytes.Split(data, []byte("\n")) {
		var rec struct {
			Timestamp string `json:"timestamp"`
			Code      string `json:"code"`
		}
		if json.Unmarshal(line, &rec) != nil || rec.Code == "" {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, rec.Timestamp)
		if err != nil {
			continue
		}
		out = append(out, LogRecord{Code: rec.Code, At: uint64(at.UnixNano()/100) + filetimeEpoch})
	}
	return out, nil
}
