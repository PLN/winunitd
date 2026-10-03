package headless

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
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
	Stage        string         `json:"stage"`
	Failure      string         `json:"failure,omitempty"`
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

// ClassifyToken names the token source a held process ran under: the
// product's genuine S4U token in session zero, an interactive session token,
// or SYSTEM's process token. Anything else is "".
func ClassifyToken(t TokenFacts) string {
	switch {
	case t.SID == SystemSID:
		return SourceProcess
	case t.Session == 0 && t.Source == productTokenSource && !t.Elevated && (t.LogonType == logonNetwork || t.LogonType == logonBatch):
		return SourceS4U
	case t.Session != 0 && !t.Elevated && (t.LogonType == logonInteractive || t.LogonType == logonRemoteInteractive || t.LogonType == logonCachedInteractive):
		return SourceWTS
	}
	return ""
}

// Validate checks a report's shape and internal consistency.
func (r *ObserverReport) Validate() error {
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
		if role != AccountA && role != AccountB || !sidPattern.MatchString(sid) || sid == SystemSID {
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
}

// Attempt is one observed launch. Exited is zero while it still runs.
type Attempt struct {
	Launched time.Time
	Exited   time.Time
	ExitCode uint32
	PID      uint32
	Created  uint64
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
	if err := r.Validate(); err != nil {
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
	want := map[string]string{ModeS4U: SourceS4U, ModeWTS: SourceWTS}[mode]
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
			if g.Role != RoleBroker && ClassifyToken(g.Token) != want {
				badToken = true
			}
		}
	}
	gap := uint64(r.MaxGap)
	if spec.Role != "" {
		gens := of(spec.Role, account)
		checkTokens(gens)
		for _, g := range gens {
			a := Attempt{Launched: FiletimeTime(g.Created), ExitCode: g.ExitCode, PID: g.PID, Created: g.Created}
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
		gs := of(role, acct)
		return len(gs) == 1 && gs[0].Seen == r.Started && gs[0].Exited == 0 && ClassifyToken(gs[0].Token) == source
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
	if badToken {
		problems = append(problems, "an observed generation did not run under the account's "+mode+" token")
	}
	return l, problems
}
