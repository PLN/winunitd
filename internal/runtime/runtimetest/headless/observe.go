package headless

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// ObserverSchema is the observer report schema.
const ObserverSchema = 1

// Roles the observer classifies. The broker is the service's winunitd.exe
// as SYSTEM; a manager is winunitd.exe under an observed account; a workload
// is the workload image under that account whose parent is one of its held
// managers; a child is the workload image under that account started by a
// held workload, such as a probe.
const (
	RoleBroker   = "broker"
	RoleManager  = "manager"
	RoleWorkload = "workload"
	RoleChild    = "child"
)

// Observer stages. Only a finished report is evidence.
const (
	ObserverRunning  = "running"
	ObserverFinished = "finished"
	ObserverFailed   = "failed"
)

// Mark names the driver can create for the observer to timestamp, and the
// implicit start mark.
const MarkStart = "start"

// MaxObservationGap bounds the time between two process scans. A process
// that lives longer than twice this cannot run unseen; a report with a
// longer gap does not support lifecycle metrics.
const MaxObservationGap = 250 * time.Millisecond

// MaxGenerations bounds a report's held processes.
const MaxGenerations = 4096

// Token logon types and the product's S4U token source.
const (
	logonInteractive       = 2
	logonNetwork           = 3
	logonBatch             = 4
	logonRemoteInteractive = 10
	logonCachedInteractive = 11
	productTokenSource     = "winunitd"
)

var (
	markPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	hexSHA256   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ObserverReport is the SYSTEM observer's raw evidence: every process of the
// watched images it held, with kernel creation and exit times, exit codes and
// token facts read from its own handles, the processes it terminated, the
// marks it timestamped and how continuously it scanned. The summary derives
// every lifecycle value from it.
type ObserverReport struct {
	Schema int `json:"schema"`
	// Executable is the SHA-256 of the observer's own executable.
	Executable string `json:"executable"`
	Boot       Boot   `json:"boot"`
	// Accounts maps the observed account roles to their SIDs.
	Accounts map[string]string `json:"accounts"`
	Plan     ObserverPlan      `json:"plan"`
	// Started and Ended are the first and last scans, as FILETIMEs.
	Started uint64 `json:"started"`
	Ended   uint64 `json:"ended"`
	Scans   int    `json:"scans"`
	// MaxGap is the longest interval between two scans, in 100 ns units.
	MaxGap       uint64         `json:"maxGap"`
	Generations  []Generation   `json:"generations"`
	Unidentified []Unidentified `json:"unidentified,omitempty"`
	Marks        []Mark         `json:"marks,omitempty"`
	Releases     []Mark         `json:"releases,omitempty"`
	// Observer is the observer's own process and token: SYSTEM in session
	// zero for qualification evidence.
	Observer ObserverIdentity `json:"observer"`
	// Images are the SHA-256 of the daemon and workload images it watched,
	// admitted against their named manifest entries before it started.
	Images ObservedImages `json:"images"`
	// Sampling records that session and logon sampling ran and succeeded;
	// without it, an empty list is unknown, not zero.
	Sampling *SamplingFacts `json:"sampling,omitempty"`
	// Sessions are the interactive sessions with a user, sampled about
	// once a second and recorded when they change; the first sample is
	// always recorded.
	Sessions []SessionSample `json:"sessions,omitempty"`
	// Logons are the watched accounts' password-bearing logon sessions the
	// observer sampled and those the Security log audited since the boot.
	Logons []LogonFact `json:"logons,omitempty"`
	// Audit records whether the audited logon history since the boot is
	// complete.
	Audit *AuditFacts `json:"audit,omitempty"`
	// Profiles and Progress are read at the last scan, per watched account.
	Profiles map[string]ProfileFacts `json:"profiles,omitempty"`
	Progress map[string]Progress     `json:"progress,omitempty"`
	Stage    string                  `json:"stage"`
	Failure  string                  `json:"failure,omitempty"`
}

// SessionSample is the set of interactive sessions with a user at At.
type SessionSample struct {
	At    uint64        `json:"at"`
	Users []SessionUser `json:"users,omitempty"`
}

// SessionUser is one interactive session and its user.
type SessionUser struct {
	Session uint32 `json:"session"`
	SID     string `json:"sid"`
	State   uint32 `json:"state"`
}

// LogonFact is one password-bearing logon of a watched account, sampled
// from LSA or read from the Security log's logon audit.
type LogonFact struct {
	ID        string `json:"id"`
	SID       string `json:"sid"`
	Type      uint32 `json:"type"`
	LogonTime uint64 `json:"logonTime"`
	// Source is "sample" or "audit"; Process is the audited logon process,
	// which separates the product's own S4U logons from password logons.
	Source  string `json:"source"`
	Process string `json:"process,omitempty"`
}

// ObserverIdentity is the observer's own process and token.
type ObserverIdentity struct {
	PID     uint32 `json:"pid"`
	Created uint64 `json:"created"`
	SID     string `json:"sid"`
	Session uint32 `json:"session"`
}

// ObservedImages are the hashes of the images the observer watched.
type ObservedImages struct {
	Daemon   string `json:"daemon"`
	Workload string `json:"workload"`
}

// SamplingFacts is how session and logon sampling went: the samples taken,
// the first and last, the longest interval between two, and every query
// that failed. A failed query leaves what it would have seen unknown.
type SamplingFacts struct {
	Samples     int           `json:"samples"`
	First       uint64        `json:"first"`
	Last        uint64        `json:"last"`
	MaxInterval uint64        `json:"maxInterval"`
	Errors      []NativeError `json:"errors,omitempty"`
}

// AuditFacts is the Security log's logon history since the boot: whether
// it was read, the oldest event it still holds and whether it was cleared
// since the boot. Only a log that reaches back past the boot, uncleared,
// lists every logon since then.
type AuditFacts struct {
	Read             bool          `json:"read"`
	Oldest           uint64        `json:"oldest,omitempty"`
	ClearedSinceBoot bool          `json:"clearedSinceBoot"`
	Errors           []NativeError `json:"errors,omitempty"`
}

// MaxSampleInterval bounds the time between two samples.
const MaxSampleInterval = 5 * time.Second

// LogonHistoryKnown reports whether the report lists every password-bearing
// logon of the watched accounts since its boot: sampling ran without error
// and the audited history reaches back past the boot, uncleared.
func (r *ObserverReport) LogonHistoryKnown() bool {
	a := r.Audit
	return r.samplingComplete() && a != nil && a.Read && len(a.Errors) == 0 && !a.ClearedSinceBoot && a.Oldest != 0 && a.Oldest <= r.Boot.Time
}

func (r *ObserverReport) samplingComplete() bool {
	sm := r.Sampling
	return sm != nil && sm.Samples >= 2 && len(sm.Errors) == 0 && sm.First >= r.Started && sm.Last <= r.Ended &&
		time.Duration(sm.MaxInterval)*100 <= MaxSampleInterval
}

// ProfileFacts is an account's profile: registered in ProfileList, the
// creation time of its directory (zero when absent) and whether its hive is
// loaded.
type ProfileFacts struct {
	// Path is the profile directory ProfileList names, private evidence.
	Path             string        `json:"path,omitempty"`
	Registered       bool          `json:"registered"`
	DirectoryCreated uint64        `json:"directoryCreated,omitempty"`
	HiveLoaded       bool          `json:"hiveLoaded"`
	Errors           []NativeError `json:"errors,omitempty"`
}

// Progress is the workload's flushed liveness record.
type Progress struct {
	Sequence         uint64 `json:"sequence"`
	Nonce            string `json:"nonce"`
	Time             string `json:"time"`
	PID              uint32 `json:"pid"`
	Created          uint64 `json:"created"`
	SID              string `json:"sid"`
	AuthenticationID string `json:"authenticationId"`
	Session          uint32 `json:"session"`
}

// PasswordLogonsSince counts the watched accounts' password-bearing logons
// that began at or after the boot's time.
func (r *ObserverReport) PasswordLogonsSince() int {
	seen := map[string]bool{}
	for _, l := range r.Logons {
		if l.LogonTime >= r.Boot.Time && !strings.EqualFold(l.Process, productTokenSource) {
			seen[l.ID] = true
		}
	}
	return len(seen)
}

// Boot identifies one boot: the kernel boot time and the boot counter.
type Boot struct {
	Time    uint64 `json:"time"`
	Counter uint32 `json:"counter"`
}

// String is the boot ID records carry.
func (b Boot) String() string { return fmt.Sprintf("boot-%d-%d", b.Counter, b.Time) }

// ObserverPlan is what the observer was asked to do, for the record.
type ObserverPlan struct {
	Target       string   `json:"target,omitempty"`
	Crash        string   `json:"crash,omitempty"`
	Lives        []string `json:"lives,omitempty"`
	Rest         string   `json:"rest,omitempty"`
	ReleaseAfter int      `json:"releaseAfter,omitempty"`
}

// Generation is one held process.
type Generation struct {
	Role      string `json:"role"`
	Account   string `json:"account,omitempty"`
	PID       uint32 `json:"pid"`
	ParentPID uint32 `json:"parentPid"`
	Created   uint64 `json:"created"`
	// Seen is the scan that first held it.
	Seen     uint64 `json:"seen"`
	Exited   uint64 `json:"exited,omitempty"`
	ExitCode uint32 `json:"exitCode,omitempty"`
	// Crashed is when the observer terminated it.
	Crashed uint64     `json:"crashed,omitempty"`
	Token   TokenFacts `json:"token"`
}

// TokenFacts is a held process's primary token.
type TokenFacts struct {
	SID              string `json:"sid"`
	Session          uint32 `json:"session"`
	Elevated         bool   `json:"elevated"`
	Source           string `json:"source"`
	LogonType        uint32 `json:"logonType,omitempty"`
	AuthPackage      string `json:"authPackage,omitempty"`
	AuthenticationID string `json:"authenticationId"`
	// ElevationType is TOKEN_ELEVATION_TYPE: 1 default, 2 full, 3 limited.
	ElevationType uint32 `json:"elevationType,omitempty"`
}

// Unidentified is a process of a watched image the observer could not hold
// and identify, such as one that exited before it was opened. It may have
// been a generation, so it leaves the report's lifecycle unproven.
type Unidentified struct {
	PID   uint32 `json:"pid"`
	At    uint64 `json:"at"`
	Win32 uint32 `json:"win32"`
}

// Mark is a named instant the observer timestamped at the scan that first
// saw it, or a release file it created.
type Mark struct {
	Name string `json:"name"`
	At   uint64 `json:"at"`
}

// FiletimeTime converts a FILETIME to UTC time.
func FiletimeTime(ft uint64) time.Time {
	const epochDelta = 116444736000000000
	return time.Unix(0, (int64(ft)-epochDelta)*100).UTC()
}

// ClassFilteredAdmin is the token class of an administrator's filtered
// interactive token.
const ClassFilteredAdmin = "filtered-admin"

// ClassifyToken names the token source a held process ran under: the
// product's genuine S4U token in session zero, a standard interactive
// session token, an administrator's filtered interactive token, or SYSTEM's
// process token. Anything else is "".
func ClassifyToken(t TokenFacts) string {
	interactive := t.LogonType == logonInteractive || t.LogonType == logonRemoteInteractive || t.LogonType == logonCachedInteractive
	switch {
	case t.SID == SystemSID:
		return SourceProcess
	case t.Session == 0 && t.Source == productTokenSource && !t.Elevated && (t.LogonType == logonNetwork || t.LogonType == logonBatch):
		return SourceS4U
	case t.Session != 0 && !t.Elevated && interactive && t.ElevationType == tokenElevationTypeLimit:
		return ClassFilteredAdmin
	case t.Session != 0 && !t.Elevated && interactive && t.ElevationType != 2:
		return SourceWTS
	}
	return ""
}

// tokenClass is the class a mode's processes must have.
func tokenClass(mode string) string {
	return map[string]string{ModeS4U: SourceS4U, ModeWTS: SourceWTS, ModeFilteredAdmin: ClassFilteredAdmin}[mode]
}

// Validate checks a report's shape and internal consistency.
// Validate checks a report as qualification evidence: its shape and
// consistency, an observer that ran as SYSTEM in session zero, the admitted
// images it watched and complete sampling.
func (r *ObserverReport) Validate() error {
	if err := r.validateStructure(); err != nil {
		return err
	}
	o := r.Observer
	if o.SID != SystemSID || o.Session != 0 || o.PID == 0 || o.Created == 0 {
		return errors.New("the observer did not run as SYSTEM in session zero")
	}
	if !hexSHA256.MatchString(r.Images.Daemon) || !hexSHA256.MatchString(r.Images.Workload) {
		return errors.New("the observer recorded no admitted images")
	}
	if !r.samplingComplete() {
		return errors.New("the observer's session and logon sampling is incomplete")
	}
	return nil
}

// validateStructure checks a report's shape and internal consistency,
// whatever account the observer ran as.
func (r *ObserverReport) validateStructure() error {
	if r.Schema != ObserverSchema {
		return fmt.Errorf("observer schema %d", r.Schema)
	}
	if r.Stage != ObserverFinished {
		return fmt.Errorf("observer stage %q", r.Stage)
	}
	if !hexSHA256.MatchString(r.Executable) {
		return errors.New("observer executable is not a SHA-256")
	}
	if r.Boot.Time == 0 {
		return errors.New("observer has no boot identity")
	}
	if r.Started == 0 || r.Ended < r.Started || r.Scans < 2 {
		return errors.New("observer did not scan")
	}
	if len(r.Generations) > MaxGenerations {
		return errors.New("too many generations")
	}
	sids := map[string]string{}
	for role, sid := range r.Accounts {
		if role != AccountA && role != AccountB && role != AccountAdmin || !sidPattern.MatchString(sid) || sid == SystemSID {
			return fmt.Errorf("observed account %s", role)
		}
		if other, dup := sids[sid]; dup {
			return fmt.Errorf("accounts %s and %s share a SID", role, other)
		}
		sids[sid] = role
	}
	type ident struct {
		pid     uint32
		created uint64
	}
	seen := map[ident]bool{}
	for i, g := range r.Generations {
		id := ident{g.PID, g.Created}
		if g.PID == 0 || g.Created == 0 || seen[id] {
			return fmt.Errorf("generation %d: identity", i)
		}
		seen[id] = true
		if g.Seen < g.Created || g.Seen < r.Started || g.Seen > r.Ended {
			return fmt.Errorf("generation %d: seen out of order", i)
		}
		if g.Exited != 0 && (g.Exited < g.Created || g.Exited > r.Ended) {
			return fmt.Errorf("generation %d: exit out of order", i)
		}
		if g.Crashed != 0 && (g.Crashed < g.Seen || g.Exited == 0 || g.Exited < g.Crashed) {
			return fmt.Errorf("generation %d: crash out of order", i)
		}
		switch g.Role {
		case RoleBroker:
			if g.Account != "" || g.Token.SID != SystemSID {
				return fmt.Errorf("generation %d: broker identity", i)
			}
		case RoleManager, RoleWorkload, RoleChild:
			if sid, ok := r.Accounts[g.Account]; !ok || g.Token.SID != sid {
				return fmt.Errorf("generation %d: account identity", i)
			}
		default:
			return fmt.Errorf("generation %d: role %q", i, g.Role)
		}
	}
	for _, m := range append(slices.Clone(r.Marks), r.Releases...) {
		if !markPattern.MatchString(m.Name) || m.Name == MarkStart || m.At < r.Started || m.At > r.Ended {
			return fmt.Errorf("mark %q", m.Name)
		}
	}
	last := uint64(0)
	for i, sm := range r.Sessions {
		if sm.At < r.Started || sm.At > r.Ended || sm.At < last {
			return fmt.Errorf("session sample %d out of order", i)
		}
		last = sm.At
	}
	watched := map[string]bool{}
	for _, sid := range r.Accounts {
		watched[sid] = true
	}
	for i, l := range r.Logons {
		if !watched[l.SID] || l.ID == "" || l.LogonTime == 0 || l.LogonTime > r.Ended || l.Source != "sample" && l.Source != "audit" {
			return fmt.Errorf("logon %d", i)
		}
	}
	for account := range r.Profiles {
		if r.Accounts[account] == "" {
			return fmt.Errorf("profile of unwatched account %s", account)
		}
	}
	for account, p := range r.Progress {
		if r.Accounts[account] == "" || p.SID != r.Accounts[account] {
			return fmt.Errorf("progress of account %s", account)
		}
	}
	return nil
}

// Lifecycle is what the summary derives from an observer report for one
// record.
type Lifecycle struct {
	Attempts    []Attempt
	Negative    *Window
	Replacement *Replacement
	// Kept, Peer and Drained are set only when the case asks for them.
	Kept    *bool
	Peer    *bool
	Drained *bool
	// Values are the boot, session and profile metrics the case asks for.
	Values map[string]float64
}

// Attempt is one observed launch. Exited is zero while it still runs.
type Attempt struct {
	Launched time.Time
	Exited   time.Time
	ExitCode uint32
	PID      uint32
	Created  uint64
	// Crashed is set when the observer terminated it.
	Crashed bool
}

// Window is a closed observation interval.
type Window struct {
	Since time.Time
	Until time.Time
}

// Process is one exact process: PID and creation FILETIME, and its exit
// FILETIME from a held handle, zero while it runs.
type Process struct {
	Role    string
	PID     uint32
	Created uint64
	Exited  uint64
}

// Replacement lists the processes alive when the observer crashed its
// target and the first replacement created after that.
type Replacement struct {
	Old []Process
	New Process
}

// DeriveLifecycle derives the lifecycle values spec asks for, for the account
// role whose SID and mode the record claims. Every generation it uses must
// have run under that account with the mode's token source. It returns the
// reasons the report cannot support the derivation.
func DeriveLifecycle(r *ObserverReport, spec ObserveSpec, account, sid, mode string) (Lifecycle, []string) {
	var l Lifecycle
	var problems []string
	if err := r.validateStructure(); err != nil {
		return l, []string{err.Error()}
	}
	if r.Accounts[account] != sid {
		return l, []string{"the observer watched another SID for account " + account}
	}
	if time.Duration(r.MaxGap)*100 > MaxObservationGap {
		problems = append(problems, "the observer's scans were too far apart")
	}
	if len(r.Unidentified) > 0 {
		problems = append(problems, fmt.Sprintf("%d processes of a watched image were not identified", len(r.Unidentified)))
	}
	want := tokenClass(mode)
	// Where the account also has an interactive session, its session
	// manager runs beside the headless one under a session token.
	allowed := []string{want}
	if spec.Session || spec.Independent {
		allowed = append(allowed, SourceWTS)
	}
	of := func(role, acct string) []Generation {
		var out []Generation
		for _, g := range r.Generations {
			if g.Role == role && g.Account == acct {
				out = append(out, g)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Created < out[j].Created })
		return out
	}
	badToken := false
	checkTokens := func(gs []Generation) {
		for _, g := range gs {
			if g.Role != RoleBroker && !slices.Contains(allowed, ClassifyToken(g.Token)) {
				badToken = true
			}
		}
	}
	gap := uint64(r.MaxGap)
	if spec.Role != "" {
		gens := of(spec.Role, account)
		checkTokens(gens)
		for _, g := range gens {
			a := Attempt{Launched: FiletimeTime(g.Created), ExitCode: g.ExitCode, PID: g.PID, Created: g.Created, Crashed: g.Crashed != 0}
			if g.Exited != 0 {
				a.Exited = FiletimeTime(g.Exited)
				// A generation that lived no longer than two scans could
				// have had siblings that ran unseen.
				if g.Exited-g.Created <= 2*gap {
					problems = append(problems, "a generation lived too briefly to rule out unseen ones")
				}
			}
			l.Attempts = append(l.Attempts, a)
		}
	}
	if spec.Negative != "" {
		since := uint64(0)
		if spec.Negative == MarkStart {
			since = r.Started
		}
		for _, m := range r.Marks {
			if m.Name == spec.Negative && since == 0 {
				since = m.At
			}
		}
		if since == 0 {
			problems = append(problems, "the observer saw no "+spec.Negative+" mark")
		} else {
			l.Negative = &Window{Since: FiletimeTime(since), Until: FiletimeTime(r.Ended)}
		}
		// An orderly stop of the broker the observer held, and a new broker
		// that started after it and still runs, all inside the window.
		if spec.Restart && since != 0 {
			brokers := of(RoleBroker, "")
			restarted := slices.ContainsFunc(brokers, func(old Generation) bool {
				return old.Exited > since && old.Crashed == 0 && slices.ContainsFunc(brokers, func(g Generation) bool {
					return g.Created >= old.Exited && g.Created < r.Ended && g.Exited == 0
				})
			})
			if !restarted {
				problems = append(problems, "the broker was not stopped and restarted inside the window")
			}
		}
	}
	if spec.Crash != "" {
		var target *Generation
		for _, g := range r.Generations {
			if g.Role == spec.Crash && g.Crashed != 0 && (g.Role == RoleBroker || g.Account == account) {
				if target == nil || g.Crashed < target.Crashed {
					target = &g
				}
			}
		}
		if target == nil {
			problems = append(problems, "the observer crashed no "+spec.Crash)
		} else {
			at := target.Crashed
			rep := &Replacement{}
			var old []Generation
			for _, role := range spec.Old {
				acct := account
				if role == RoleBroker {
					acct = ""
				}
				for _, g := range of(role, acct) {
					if g.Seen <= at && (g.Exited == 0 || g.Exited >= at) {
						old = append(old, g)
						rep.Old = append(rep.Old, Process{Role: g.Role, PID: g.PID, Created: g.Created, Exited: g.Exited})
					}
				}
			}
			checkTokens(old)
			for _, g := range of(spec.New, account) {
				if g.Created > at {
					checkTokens([]Generation{g})
					rep.New = Process{Role: g.Role, PID: g.PID, Created: g.Created, Exited: g.Exited}
					break
				}
			}
			l.Replacement = rep
		}
	}
	// stable is one process of the role, held from the first scan and
	// still running at the last, with the expected token.
	stable := func(role, acct, source string) bool {
		var gs []Generation
		for _, g := range of(role, acct) {
			if ClassifyToken(g.Token) == source {
				gs = append(gs, g)
			}
		}
		return len(gs) == 1 && gs[0].Seen == r.Started && gs[0].Exited == 0
	}
	if len(spec.Kept) > 0 {
		kept := true
		for _, role := range spec.Kept {
			kept = kept && stable(role, account, want)
		}
		l.Kept = &kept
	}
	if spec.Peer {
		ok := false
		for peer, psid := range r.Accounts {
			if peer == account {
				continue
			}
			ok = psid != "" && stable(RoleManager, peer, SourceS4U) && stable(RoleWorkload, peer, SourceS4U)
		}
		l.Peer = &ok
	}
	if spec.Drained {
		drained := true
		for _, g := range r.Generations {
			if g.Account == account && g.Exited == 0 {
				drained = false
			}
		}
		l.Drained = &drained
	}
	if spec.Independent {
		for k, v := range independent(r, account, sid) {
			if l.Values == nil {
				l.Values = map[string]float64{}
			}
			l.Values[k] = v
		}
	}
	if spec.Boot != "" || spec.Session || spec.Unloaded {
		if l.Values == nil {
			l.Values = map[string]float64{}
		}
		maxUsers, accountSeen := 0, false
		for _, sm := range r.Sessions {
			maxUsers = max(maxUsers, len(sm.Users))
			for _, u := range sm.Users {
				if u.SID == sid {
					accountSeen = true
				}
			}
		}
		if len(r.Sessions) > 0 {
			l.Values["interactiveSessions"] = float64(maxUsers)
			last := r.Sessions[len(r.Sessions)-1]
			l.Values["sessionCycle"] = boolMetric(accountSeen && !slices.ContainsFunc(last.Users, func(u SessionUser) bool { return u.SID == sid }))
		}
		prof, hasProfile := r.Profiles[account]
		if spec.Unloaded && hasProfile && len(prof.Errors) == 0 {
			l.Values["profileUnloaded"] = boolMetric(!prof.HiveLoaded)
		}
		if spec.Boot != "" {
			started, progressing := bootStart(r, account, sid)
			l.Values["bootStarted"] = boolMetric(started != nil)
			l.Values["progressing"] = boolMetric(progressing)
			if hasProfile && len(prof.Errors) == 0 {
				created := prof.Registered && prof.HiveLoaded && prof.DirectoryCreated > r.Boot.Time
				existing := prof.Registered && prof.HiveLoaded && prof.DirectoryCreated != 0 && prof.DirectoryCreated < r.Boot.Time
				l.Values["profileCreated"] = boolMetric(spec.Boot == BootCreated && created)
				l.Values["profileExisting"] = boolMetric(spec.Boot == BootExisting && existing)
			}
		}
	}
	if badToken {
		problems = append(problems, "an observed generation did not run under the account's "+mode+" token")
	}
	return l, problems
}

// The marks H06's driver creates, in this order.
const (
	MarkAdmissionRevoked = "admission-revoked"
	MarkLingerDisabled   = "linger-disabled"
	MarkLogoff           = "logoff"
)

// independent derives H06's sequence. Linger stays effective when only
// interactive admission is revoked: a headless S4U manager running at that
// mark still runs at the linger mark. A session-backed manager outlives
// disabling linger while the account's session remains: a session manager
// running at the linger mark still runs at logoff, and the session is seen
// between them. Final drain and the quiet window after logoff are the
// drained and negative metrics.
func independent(r *ObserverReport, account, sid string) map[string]float64 {
	marks := map[string]uint64{}
	for _, m := range r.Marks {
		if _, dup := marks[m.Name]; !dup {
			marks[m.Name] = m.At
		}
	}
	admission, linger, logoff := marks[MarkAdmissionRevoked], marks[MarkLingerDisabled], marks[MarkLogoff]
	if admission == 0 || linger <= admission || logoff <= linger {
		return nil
	}
	aliveAcross := func(class string, from, to uint64) bool {
		return slices.ContainsFunc(r.Generations, func(g Generation) bool {
			return g.Role == RoleManager && g.Account == account && ClassifyToken(g.Token) == class && g.Seen <= from && (g.Exited == 0 || g.Exited >= to)
		})
	}
	// Samples are recorded when the sessions change, so the last one at or
	// before the linger mark is the state at that mark.
	sessionSeen := false
	for _, sm := range r.Sessions {
		if sm.At > linger {
			break
		}
		sessionSeen = slices.ContainsFunc(sm.Users, func(u SessionUser) bool { return u.SID == sid })
	}
	return map[string]float64{
		"lingerKept":      boolMetric(aliveAcross(SourceS4U, admission, linger)),
		"sessionRetained": boolMetric(aliveAcross(SourceWTS, linger, logoff) && sessionSeen),
	}
}

// Kinds of cold-boot profile.
const (
	BootCreated  = "created"
	BootExisting = "existing"
)

// bootStart finds the account's S4U manager and the workload it started,
// both created on this boot and still running at the end, and whether the
// workload's own progress record names that workload incarnation, its token
// and more than one flush.
func bootStart(r *ObserverReport, account, sid string) (*Generation, bool) {
	for i, m := range r.Generations {
		if m.Role != RoleManager || m.Account != account || m.Created <= r.Boot.Time || m.Exited != 0 || ClassifyToken(m.Token) != SourceS4U {
			continue
		}
		for j, w := range r.Generations {
			if w.Role != RoleWorkload || w.Account != account || w.ParentPID != m.PID || w.Created < m.Created || w.Exited != 0 ||
				ClassifyToken(w.Token) != SourceS4U {
				continue
			}
			_ = j
			p, ok := r.Progress[account]
			progressing := ok && p.PID == w.PID && p.Created == w.Created && p.SID == sid && p.AuthenticationID == w.Token.AuthenticationID &&
				p.Session == 0 && p.Sequence >= 2
			return &r.Generations[i], progressing
		}
	}
	return nil, false
}
