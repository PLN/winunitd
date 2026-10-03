package headless

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/PLN/winunitd/internal/protocol"
)

// Proof kinds: what a record must carry, beyond its result, before the
// summary accepts it. Every executed record and control declares one; a
// record without its proof, or with a proof that does not hold, stays
// incomplete whatever its result says.
const (
	// ProofObserver: lifecycle values derived from the SYSTEM observer.
	ProofObserver = "observer"
	// ProofProbe: the workload's probe results, from a process the
	// observer held as part of the account's unit, with its own token.
	ProofProbe = "probe"
	// ProofFirstUse: the preboot check that the account was never used,
	// bound to the boot that follows.
	ProofFirstUse = "first-use"
	// ProofNamedTest: a receipt of the named native test run.
	ProofNamedTest = "named-test"
	// ProofReceipt: the echo peer's own record of the nonce.
	ProofReceipt = "receipt"
	// ProofPrincipal: the SMB server's attribution of an access.
	ProofPrincipal = "principal"
	// ProofPassword: the same account's password-bearing operation on the
	// same target.
	ProofPassword = "password"
	// ProofEndpoint: SYSTEM's identification of the live pipe servers.
	ProofEndpoint = "endpoint"
	// ProofDaemonLog: a manager's diagnostics before and after a declared
	// intervention, or the system manager's protected diagnostics.
	ProofDaemonLog = "daemon-log"
	// ProofSessionProbe: probe results from the account's own interactive
	// session token, outside any unit.
	ProofSessionProbe = "session-probe"
	// ProofInventory: SYSTEM's final inventory against the baseline's.
	ProofInventory = "inventory"
	// ProofPending: no producer or validator exists yet; never accepted.
	ProofPending = "pending"
)

var baselinePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// FirstUseProof is SYSTEM's check, immediately before the cold boot, that
// the account has no profile registration, profile directory, loaded hive
// or logon session, taken on the sealed baseline it names.
type FirstUseProof struct {
	SID      string `json:"sid"`
	Baseline string `json:"baseline"`
	// BaselineSHA256 is the hash of the baseline receipt's bytes.
	BaselineSHA256   string `json:"baselineSha256"`
	Boot             Boot   `json:"boot"`
	At               uint64 `json:"at"`
	ProfileList      bool   `json:"profileList"`
	ProfileDirectory bool   `json:"profileDirectory"`
	HiveLoaded       bool   `json:"hiveLoaded"`
	LogonSessions    int    `json:"logonSessions"`
	// Errors are the queries that failed; any failure leaves absence
	// unproven.
	Errors []NativeError `json:"errors,omitempty"`
}

// CheckFirstUse validates the check for the account's SID and binds it to
// the observed cold boot: the very next boot, after the check.
func CheckFirstUse(p *FirstUseProof, sid string, boot *ObserverReport) []string {
	if p == nil {
		return []string{"no first-use proof"}
	}
	var problems []string
	switch {
	case p.SID != sid:
		problems = append(problems, "the check names another account")
	case len(p.Errors) > 0:
		problems = append(problems, "a first-use query failed")
	case p.ProfileList || p.ProfileDirectory || p.HiveLoaded || p.LogonSessions != 0:
		problems = append(problems, "the account was already used")
	}
	if !baselinePattern.MatchString(p.Baseline) || !hexSHA256.MatchString(p.BaselineSHA256) {
		problems = append(problems, "the check names no baseline")
	}
	switch {
	case p.Boot.Time == 0 || p.At <= p.Boot.Time:
		problems = append(problems, "the check has no boot")
	case boot == nil:
		problems = append(problems, "no observed cold boot follows the check")
	case boot.Boot.Counter != p.Boot.Counter+1 || boot.Boot.Time <= p.At:
		problems = append(problems, "the observed cold boot is not the boot after the check")
	}
	return problems
}

// TestEvent is one go test -json action for a test.
type TestEvent struct {
	Action string `json:"action"`
	Test   string `json:"test"`
}

// RunnerFacts is a process of a native test run and its token: the
// recorder that launched the test binary, or the test binary's own process,
// the owner of the tests it runs.
type RunnerFacts struct {
	ID      string     `json:"id,omitempty"`
	PID     uint32     `json:"pid"`
	Created uint64     `json:"created"`
	Token   TokenFacts `json:"token"`
}

// SubjectReport is what a native test writes about the S4U subject it
// launched: the test's name, its own process incarnation and the subject
// process's incarnation and token.
type SubjectReport struct {
	Test         string     `json:"test"`
	OwnerPID     uint32     `json:"ownerPid"`
	OwnerCreated uint64     `json:"ownerCreated"`
	PID          uint32     `json:"pid"`
	Created      uint64     `json:"created"`
	Token        TokenFacts `json:"token"`
}

// TestRunProof is the receipt of one native test run: the admitted test
// binary, the recorder that ran it and the test process itself with their
// tokens, the test process's exit code, every test and package action, and,
// for a test that launches the account's S4U subject, that subject as the
// test observed it.
type TestRunProof struct {
	Artifact string         `json:"artifact"`
	SHA256   string         `json:"sha256"`
	Runner   RunnerFacts    `json:"runner"`
	Owner    RunnerFacts    `json:"owner"`
	ExitCode int            `json:"exitCode"`
	Events   []TestEvent    `json:"events"`
	Subject  *SubjectReport `json:"subject,omitempty"`
	Native   []NativeError  `json:"native,omitempty"`
}

// CheckTestRun validates a receipt for one entry: the admitted binary of
// the entry's package; the record's recorder and the test process it
// launched, in one context (SYSTEM in session zero, or the account's own
// session token for a session lane); exactly one run of each named test
// that passed with nothing skipped or failed and no other test; a package
// that finished with a pass and a test process that exited 0; and, for an
// S4U entry, a genuine S4U subject of the account that this test, in this
// test process, launched.
func CheckTestRun(p *TestRunProof, e Entry, runnerID, sid string, run AdmittedRun) []string {
	if p == nil {
		return []string{"no test receipt"}
	}
	var problems []string
	want := e.Package + ".test.exe"
	if p.Artifact != want || run.Manifest.Lookup(want) == "" || run.Manifest.Lookup(want) != p.SHA256 {
		problems = append(problems, "the test binary is not the admitted "+want)
	}
	if p.Runner.ID != runnerID || p.Runner.PID == 0 || p.Runner.Created == 0 {
		problems = append(problems, "the receipt names another runner")
	}
	o := p.Owner
	if o.PID == 0 || o.Created == 0 || o.Created < p.Runner.Created || o.PID == p.Runner.PID {
		problems = append(problems, "the receipt names no test process launched by its recorder")
	}
	// The test process runs in its recorder's context.
	if o.Token.SID != p.Runner.Token.SID || o.Token.Session != p.Runner.Token.Session || o.Token.AuthenticationID == "" ||
		o.Token.AuthenticationID != p.Runner.Token.AuthenticationID {
		problems = append(problems, "the test process ran in another context than its recorder")
	}
	// S4U and SYSTEM tests run in a SYSTEM test process; a session lane runs
	// in the account's own interactive token, so a test that needs a
	// non-SYSTEM identity cannot pass by skipping.
	switch e.Mode {
	case ModeWTS, ModeFilteredAdmin:
		if o.Token.SID != sid || ClassifyToken(o.Token) != tokenClass(e.Mode) {
			problems = append(problems, "the runner is not the account's own "+e.Mode+" token")
		}
	default:
		if o.Token.SID != SystemSID || o.Token.Session != 0 {
			problems = append(problems, "the runner is not SYSTEM in session zero")
		}
	}
	tests := e.Tests
	if len(tests) == 0 {
		tests = []string{e.Test}
	}
	runs, passed := map[string]int{}, map[string]bool{}
	var pkg []string
	for _, ev := range p.Events {
		if ev.Test == "" {
			pkg = append(pkg, ev.Action)
			continue
		}
		top, _, _ := strings.Cut(ev.Test, "/")
		if !slices.Contains(tests, top) {
			problems = append(problems, "the receipt covers another test")
			break
		}
		switch ev.Action {
		case "run":
			if ev.Test == top {
				runs[top]++
			}
		case "pass":
			if ev.Test == top {
				passed[top] = true
			}
		case "skip", "fail":
			problems = append(problems, "the test or a subtest was "+map[string]string{"skip": "skipped", "fail": "failed"}[ev.Action])
		}
	}
	for _, name := range tests {
		if runs[name] != 1 || !passed[name] {
			problems = append(problems, "the named test did not run once and pass")
			break
		}
	}
	// The package's own result is its one terminal action, the last event.
	if len(pkg) != 1 || pkg[0] != "pass" || len(p.Events) == 0 || p.Events[len(p.Events)-1].Test != "" {
		problems = append(problems, "the test package did not finish with a pass")
	}
	if p.ExitCode != 0 {
		problems = append(problems, "the test process did not exit successfully")
	}
	if e.Mode == ModeS4U {
		s := p.Subject
		switch {
		case s == nil:
			problems = append(problems, "the test reported no S4U subject")
		case !slices.Contains(tests, strings.SplitN(s.Test, "/", 2)[0]) || s.OwnerPID != o.PID || s.OwnerCreated != o.Created ||
			s.PID == 0 || s.Created <= o.Created:
			problems = append(problems, "the subject report is not this run's")
		case s.Token.SID != sid || ClassifyToken(s.Token) != SourceS4U || s.Token.AuthenticationID == "" ||
			s.Token.AuthenticationID == o.Token.AuthenticationID:
			problems = append(problems, "the test's subject is not the account's genuine S4U token")
		}
	}
	if len(p.Native) > 0 {
		problems = append(problems, "the receipt recorded a native error")
	}
	return problems
}

// goTestEvent is the part of a go test -json line the receipt keeps.
type goTestEvent struct {
	Action string
	Test   string
}

// ParseTestEvents reads go test -json output and keeps every run, pass,
// fail and skip of a test and the package's own pass, fail or skip, which
// has no test name. A line that is not a JSON object fails.
func ParseTestEvents(r io.Reader) ([]TestEvent, error) {
	var out []TestEvent
	sc := bufio.NewScanner(io.LimitReader(r, MaxFileBytes))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ev goTestEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			return nil, errors.New("test output is not go test -json")
		}
		switch ev.Action {
		case "run", "pass", "fail", "skip":
			if ev.Action != "run" || ev.Test != "" {
				out = append(out, TestEvent(ev))
			}
		}
		if len(out) > 4096 {
			return nil, errors.New("too many test events")
		}
	}
	return out, sc.Err()
}

// UnitStatusProof is a unit status snapshot from winctl: its state,
// reason, restart attempt and restart budget.
type UnitStatusProof struct {
	Unit           string        `json:"unit"`
	ActiveState    string        `json:"activeState"`
	Reason         string        `json:"reason,omitempty"`
	RestartAttempt uint32        `json:"restartAttempt"`
	MainPID        uint32        `json:"mainPid,omitempty"`
	Budget         *StatusBudget `json:"budget,omitempty"`
	At             uint64        `json:"at"`
}

// StatusLag bounds how long after the observation a status snapshot may be
// read.
const StatusLag = 5 * time.Minute

// CheckStatus binds a unit status snapshot to the observation: read during
// it or just after, and, for an active unit, naming as its main process the
// account's running workload the observer held.
func CheckStatus(s *UnitStatusProof, rep *ObserverReport, account string) []string {
	if s == nil || rep == nil {
		return nil
	}
	var problems []string
	if s.At < rep.Started || s.At > rep.Ended+uint64(StatusLag/100) {
		problems = append(problems, "the status was not read during or just after the observation")
	}
	if s.ActiveState == "active" && !slices.ContainsFunc(rep.Generations, func(g Generation) bool {
		return g.Role == RoleWorkload && g.Account == account && g.Exited == 0 && s.MainPID != 0 && g.PID == s.MainPID
	}) {
		problems = append(problems, "the active unit's main process is not the running workload the observer held")
	}
	return problems
}

// StatusBudget is the snapshot's start-limit budget.
type StatusBudget struct {
	Policy      string  `json:"policy,omitempty"`
	IntervalSec float64 `json:"intervalSec"`
	Burst       int     `json:"burst"`
	Remaining   *int    `json:"remaining,omitempty"`
}

// EndpointServer is the live process behind a pipe, as SYSTEM found it.
type EndpointServer struct {
	PID     uint32 `json:"pid"`
	Created uint64 `json:"created"`
	SID     string `json:"sid"`
	Image   string `json:"image"`
}

// EndpointHealth identifies one H16 pipe's server before and after the
// account's denial checks.
type EndpointHealth struct {
	Client string         `json:"client"`
	Pipe   string         `json:"pipe"`
	Before EndpointServer `json:"before"`
	After  EndpointServer `json:"after"`
}

// CheckEndpoints requires SYSTEM to have found each pipe the account was
// denied served, by the same live process before and after, and that
// process to be the genuine endpoint: the fixture's SYSTEM pipe, the peer
// account's workload pipe and the broker's control and maintenance pipes.
func CheckEndpoints(health []EndpointHealth, peerSID string) []string {
	type want struct{ pipe, sid, image string }
	wants := map[string]want{
		ClientSystemOnly:      {"", SystemSID, workloadImage},
		ClientPeerUserPipe:    {WorkloadPipe(peerSID), peerSID, workloadImage},
		ClientControlPipe:     {protocol.DefaultPipeName, SystemSID, daemonImage},
		ClientMaintenancePipe: {protocol.MaintenancePipeName, SystemSID, daemonImage},
	}
	got := map[string]EndpointHealth{}
	for _, h := range health {
		if _, dup := got[h.Client]; dup {
			return []string{"endpoint " + h.Client + " repeated"}
		}
		got[h.Client] = h
	}
	var problems []string
	for _, client := range []string{ClientSystemOnly, ClientPeerUserPipe, ClientControlPipe, ClientMaintenancePipe} {
		w, h := wants[client], got[client]
		pipeOK := h.Pipe == w.pipe || w.pipe == "" && strings.HasPrefix(h.Pipe, QualificationPipePrefix)
		switch {
		case h.Client == "":
			problems = append(problems, "endpoint "+client+" missing")
		case !pipeOK:
			problems = append(problems, "endpoint "+client+" is another pipe")
		case h.Before.PID == 0 || h.Before.Created == 0 || h.Before != h.After:
			problems = append(problems, "endpoint "+client+" was not the same live server before and after")
		case h.Before.SID != w.sid || !strings.EqualFold(h.Before.Image, w.image):
			problems = append(problems, "endpoint "+client+" is not served by its genuine server")
		}
	}
	return problems
}

// The installed images the endpoint and observer checks name.
const (
	daemonImage   = "winunitd.exe"
	workloadImage = "headless-workload.exe"
)

// CheckSubject binds a probe's token to the observer: the probing process
// must be a process the observer held as the account's workload or one of
// its children, and the probe's view of its own token must agree with the
// observer's, under the mode's token source.
func CheckSubject(rep *ObserverReport, tp *TokenProbe, account, mode string) []string {
	if tp == nil {
		return []string{"no token probe"}
	}
	if rep == nil {
		return []string{"no observer report holds the probing process"}
	}
	var held *Generation
	for i, g := range rep.Generations {
		if g.PID == tp.PID && g.Created == tp.Created && g.Account == account && (g.Role == RoleWorkload || g.Role == RoleChild) {
			held = &rep.Generations[i]
		}
	}
	if held == nil {
		return []string{"the probing process is not a held process of the account's unit"}
	}
	t := held.Token
	if tp.SID != t.SID || tp.Session != t.Session || tp.Elevated != t.Elevated || tp.AuthenticationID != t.AuthenticationID ||
		tp.Source != t.Source || tp.LogonType != t.LogonType {
		return []string{"the token probe disagrees with the held process's token"}
	}
	if want := tokenClass(mode); ClassifyToken(t) != want {
		return []string{"the probing process did not run under the account's " + mode + " token"}
	}
	return nil
}

// Token groups and elevation types a filtered administrator shows.
const (
	builtinAdministrators   = "S-1-5-32-544"
	groupUseForDenyOnly     = 0x10
	tokenElevationTypeLimit = 3
)

// FilteredAdministrator reports whether a probed token is an administrator's
// filtered token: limited elevation type, not elevated, and Administrators
// present only for deny.
func FilteredAdministrator(tp *TokenProbe) bool {
	if tp == nil || tp.Elevated || tp.ElevationType != tokenElevationTypeLimit {
		return false
	}
	return slices.ContainsFunc(tp.Groups, func(g TokenGroup) bool {
		return g.SID == builtinAdministrators && g.Attributes&groupUseForDenyOnly != 0
	})
}

// Password-bearing logon types: interactive, batch, network cleartext,
// remote interactive and cached interactive.
var passwordLogonTypes = []uint32{logonInteractive, logonBatch, 8, logonRemoteInteractive, logonCachedInteractive}

// PasswordToken reports whether a probed token is a password-bearing logon
// of sid, not the product's S4U token.
func PasswordToken(tp *TokenProbe, sid string) bool {
	return tp != nil && tp.SID == sid && tp.Source != productTokenSource && slices.Contains(passwordLogonTypes, tp.LogonType)
}

// headerMatches compares a probed token with the record's token context.
func headerMatches(t *Token, tp *TokenProbe) error {
	if t == nil || tp == nil {
		return nil
	}
	if t.SID != tp.SID || t.Session != tp.Session || t.Elevated != tp.Elevated {
		return fmt.Errorf("the record's token context disagrees with its token probe")
	}
	return nil
}
