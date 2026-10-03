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

// DaemonLogLag bounds how long after the observation the final daemon-log
// facts may be read.
const DaemonLogLag = 2 * time.Minute

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
	// ManagersRunning counts the account's manager processes when the
	// facts were read; a stopped log has none.
	ManagersRunning int `json:"managersRunning"`
}

// DaemonLogProof is the diagnostics of the manager that runs as SID, before
// the declared intervention (padding the stopped log, or preparing a legacy
// directory) and after the manager started again.
type DaemonLogProof struct {
	SID string `json:"sid"`
	// Root is the manager's data root the facts were read under.
	Root   string          `json:"root"`
	Before *DaemonLogFacts `json:"before,omitempty"`
	After  DaemonLogFacts  `json:"after"`
}

// legacyDACL reports whether a DACL is the declared legacy descriptor: not
// protected, no deny entry, full control for the account and write for an
// ordinary principal (Users, Authenticated Users or Everyone).
func legacyDACL(dacl, sid string) bool {
	if !strings.HasPrefix(dacl, "D:") {
		return false
	}
	flags := dacl[2:]
	if i := strings.IndexByte(flags, '('); i >= 0 {
		flags = flags[:i]
	}
	if strings.Contains(flags, "P") {
		return false
	}
	aces := aceToken.FindAllString(dacl, -1)
	if slices.ContainsFunc(aces, func(a string) bool { return !strings.HasPrefix(a, "(A;") }) {
		return false
	}
	grants := func(who string) bool {
		return slices.ContainsFunc(aces, func(a string) bool {
			f := strings.Split(strings.Trim(a, "()"), ";")
			return len(f) == 6 && f[0] == "A" && (f[2] == "FA" || f[2] == "0x1301bf") && f[5] == who
		})
	}
	return grants(sid) && (grants("BU") || grants("AU") || grants("WD"))
}

// fileFacts reports whether a log file's facts are coherent: a size and a
// content hash.
func fileFacts(o *ObjectFacts) bool {
	return o != nil && o.Size >= 0 && hexSHA256.MatchString(o.SHA256)
}

// daemonRoot is the user manager's data root under a profile directory.
func daemonRoot(profile string) string {
	return strings.ToLower(strings.TrimRight(profile, `\`) + `\AppData\Local\winunitd`)
}

// systemDaemonRoot is the system manager's default data root.
var systemDaemonRoot = regexp.MustCompile(`(?i)^[a-z]:\\programdata\\winunitd$`)

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
	if a.At == 0 || p.Root == "" {
		add("the daemon-log facts have no time or root")
	}
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
	for _, f := range []*ObjectFacts{a.Current, a.Archive} {
		if f != nil && !fileFacts(f) {
			add("a log file has an incoherent size or hash")
		}
	}
	// The newest open record no later than these facts were read.
	opened := func(after uint64) (uint64, bool) {
		var at uint64
		for _, r := range a.Tail {
			if r.Code == daemonOpenCode && r.At > after && r.At <= a.At && r.At > at {
				at = r.At
			}
		}
		return at, at != 0
	}
	switch check {
	case CheckProtection:
		if sid == SystemSID && !systemDaemonRoot.MatchString(p.Root) {
			add("the daemon-log facts are not the system manager's data root")
		}
		protected("daemon directory", a.Dir)
		protected("current log", a.Current)
		if a.Archive != nil {
			protected("archive", a.Archive)
		}
		if _, ok := opened(0); !ok {
			add("the log has no daemon.open record before the facts were read")
		}
		return problems
	case CheckRotation, CheckRepair:
	default:
		return append(problems, "unknown daemon-log check "+check)
	}
	b := p.Before
	if b == nil || b.At == 0 || b.At >= a.At {
		return append(problems, "no daemon-log facts before the intervention")
	}
	if b.ManagersRunning != 0 {
		add("the account's manager was running when the log was prepared")
	}
	if check == CheckRotation {
		switch {
		case !fileFacts(b.Current) || b.Current.Size < RotationBytes:
			add("the stopped log was not padded past the rotation size")
		case a.Archive == nil || a.Archive.SHA256 != b.Current.SHA256 || a.Archive.Size != b.Current.Size:
			add("the padded log was not rotated to the archive unchanged")
		case !fileFacts(a.Current) || a.Current.Size >= RotationBytes:
			add("no fresh current log after the rotation")
		}
		protected("archive", a.Archive)
	} else if b.Dir == nil || b.Dir.Owner != sid || !legacyDACL(b.Dir.DACL, sid) {
		add("the directory was not the declared legacy account-owned, openly writable directory before the first start")
	}
	protected("daemon directory", a.Dir)
	protected("current log", a.Current)
	openAt, ok := opened(b.At)
	if !ok {
		add("no daemon.open record after the intervention")
	}
	if rep == nil {
		return append(problems, "no observer report of the manager's start")
	}
	// The facts belong to the manager the observer saw: its data root under
	// the account's profile, its open record within the observation and
	// after that manager's creation.
	if prof, found := rep.Profiles[account]; !found || prof.Path == "" || strings.ToLower(p.Root) != daemonRoot(prof.Path) {
		add("the daemon-log facts are not the account's manager's data root")
	}
	if ok && (openAt < rep.Started || openAt > rep.Ended) {
		add("the open record is outside the observation")
	}
	if a.At < rep.Ended || a.At > rep.Ended+uint64(DaemonLogLag/100) {
		add("the final facts were not read right after the observation")
	}
	started := slices.ContainsFunc(rep.Generations, func(g Generation) bool {
		return g.Role == RoleManager && g.Account == account && g.Created > b.At && (!ok || g.Created <= openAt) && g.Exited == 0 &&
			ClassifyToken(g.Token) == tokenClass(mode)
	})
	if !started {
		add("the observer held no manager of the account started after the intervention, before its open record and still running")
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

// Inventory resources: what the inventory reads. Each must be read for the
// inventory to cover the fixture.
const (
	ResourceProcesses = "processes"
	ResourcePipes     = "pipes"
	ResourceTasks     = "tasks"
	ResourceFirewall  = "firewall"
	ResourceGrants    = "grants"
	ResourceTemplate  = "template"
	ResourceService   = "service"
	ResourceDataACL   = "data-acl"
	ResourceAdmission = "admission"
)

var (
	// MachineResources is the machine state a baseline receipt must cover.
	MachineResources = []string{ResourceTasks, ResourceFirewall, ResourceGrants, ResourceTemplate, ResourceService, ResourceDataACL, ResourceAdmission}
	// InventoryResources is every resource a final inventory must cover.
	InventoryResources = append([]string{ResourceProcesses, ResourcePipes}, MachineResources...)
)

// InventoryScope is what an inventory covered: the accounts whose grants it
// read, the images whose processes it listed and the resources it read.
type InventoryScope struct {
	Accounts  []string `json:"accounts"`
	Images    []string `json:"images"`
	Resources []string `json:"resources"`
}

// MachineFacts is the machine state the qualification may change and must
// restore: the winunitd service configuration, the system data root's and
// linger directory's security, the interactive admission policy file (nil
// when absent), the scope accounts' linger grants, and the fixture's
// scheduled tasks, firewall rules and Default profile template files.
type MachineFacts struct {
	Service       ServiceFacts `json:"service"`
	DataDir       *ObjectFacts `json:"dataDir,omitempty"`
	Linger        *ObjectFacts `json:"linger,omitempty"`
	Admission     *ObjectFacts `json:"admission,omitempty"`
	Grants        []string     `json:"grants,omitempty"`
	Tasks         []string     `json:"tasks,omitempty"`
	FirewallRules []string     `json:"firewallRules,omitempty"`
	TemplateFiles []string     `json:"templateFiles,omitempty"`
}

func (f *MachineFacts) equal(o *MachineFacts) bool {
	obj := func(a, b *ObjectFacts) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
	return f.Service == o.Service && obj(f.DataDir, o.DataDir) && obj(f.Linger, o.Linger) && obj(f.Admission, o.Admission) &&
		slices.Equal(f.Grants, o.Grants) && slices.Equal(f.Tasks, o.Tasks) && slices.Equal(f.FirewallRules, o.FirewallRules) &&
		slices.Equal(f.TemplateFiles, o.TemplateFiles)
}

// InventoryBaseline is SYSTEM's receipt of the machine state before any
// case ran. The first-use check records its hash; the final inventory
// embeds it with the hash of the bytes it read.
type InventoryBaseline struct {
	Name  string         `json:"name"`
	At    uint64         `json:"at"`
	Boot  Boot           `json:"boot"`
	Scope InventoryScope `json:"scope"`
	Facts MachineFacts   `json:"facts"`
}

// InventoryProof is SYSTEM's final inventory before the snapshot revert:
// the baseline receipt, what remains of the fixture's processes and pipes
// besides the service's own process, and the machine state, which must
// equal the baseline's.
type InventoryProof struct {
	Baseline       InventoryBaseline `json:"baseline"`
	BaselineSHA256 string            `json:"baselineSha256"`
	At             uint64            `json:"at"`
	Boot           Boot              `json:"boot"`
	Scope          InventoryScope    `json:"scope"`
	// Broker is the process the service control manager names as the
	// running winunitd service; it alone may remain.
	Broker    *InventoryProcess  `json:"broker,omitempty"`
	Processes []InventoryProcess `json:"processes,omitempty"`
	Pipes     []string           `json:"pipes,omitempty"`
	Facts     MachineFacts       `json:"facts"`
	Errors    []NativeError      `json:"errors,omitempty"`
}

// InventoryProcess is one process of an inventoried image.
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

// InventoryContext is what the rest of the run fixes for the final
// inventory: the first-use check that recorded the baseline, the latest
// observation end of any case, and the accounts and images the run used.
type InventoryContext struct {
	FirstUse *FirstUseProof
	After    uint64
	Accounts []string
	Images   []string
}

// CheckInventory requires a complete inventory bound to the run's baseline
// and after its last observation: nothing of the fixture remains besides
// the installed service's process, and the machine state equals the
// baseline's.
func CheckInventory(p *InventoryProof, c InventoryContext) []string {
	if p == nil {
		return []string{"no final inventory"}
	}
	var problems []string
	add := func(s string) { problems = append(problems, s) }
	b := &p.Baseline
	switch {
	case c.FirstUse == nil:
		add("no first-use check recorded the baseline")
	case !baselinePattern.MatchString(b.Name) || b.Name != c.FirstUse.Baseline || !hexSHA256.MatchString(p.BaselineSHA256) ||
		p.BaselineSHA256 != c.FirstUse.BaselineSHA256:
		add("the inventory's baseline is not the one the first-use check recorded")
	case b.At == 0 || b.At >= c.FirstUse.At || b.Boot.Time == 0 || b.Boot.Counter > c.FirstUse.Boot.Counter:
		add("the baseline was not taken before the first case")
	}
	if p.At == 0 || p.At <= b.At || p.At <= c.After {
		add("the inventory was not taken after the last observation")
	}
	if len(p.Errors) > 0 {
		add("an inventory query failed")
	}
	covers := func(have, want []string) bool {
		return !slices.ContainsFunc(want, func(w string) bool {
			return !slices.ContainsFunc(have, func(h string) bool { return strings.EqualFold(h, w) })
		})
	}
	if !covers(p.Scope.Accounts, c.Accounts) || !covers(p.Scope.Images, c.Images) || !covers(p.Scope.Resources, InventoryResources) ||
		!covers(b.Scope.Accounts, c.Accounts) || !covers(b.Scope.Resources, MachineResources) {
		add("the inventory does not cover every account, image and resource of the run")
	}
	switch br := p.Broker; {
	case !p.Facts.Service.Installed:
		add("the winunitd service is not installed")
	case br == nil || br.PID == 0 || br.SID != SystemSID || !strings.EqualFold(br.Image, daemonImage):
		add("the service control manager names no running winunitd service process")
	}
	if len(p.Processes) > 0 {
		add("owned processes remain")
	}
	if len(p.Pipes) > 0 {
		add("fixture pipes remain")
	}
	if p.Facts.DataDir == nil {
		add("the data root's security was not read")
	}
	if !p.Facts.equal(&b.Facts) {
		add("the machine state differs from the baseline")
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
