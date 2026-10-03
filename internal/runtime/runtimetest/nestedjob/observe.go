package nestedjob

import (
	"errors"
	"fmt"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"time"
)

// Installed-daemon lane (N06, N07). The observer holds the old tree and any
// named manager processes, terminates the exact crash target, waits for the
// replacement generation and records kernel exit times of every old process
// and the replacement MAIN's creation time. That exit-before-creation order
// proves native process ordering at the replacement launch; it is a
// different claim from the manager's internal admission boundary pinned by
// N05. The replacement MAIN's own check of the previous generation is an
// entry observation, reported separately. The recorder trusts nothing the
// report concludes: it recomputes every check from the raw identities.

// ObserverReportSchema is the observer report schema.
const ObserverReportSchema = 2

// Observer report stages.
const (
	StageObserved = "observed"
	StageReplaced = "replaced"
	StageFinished = "finished"
	StageFailed   = "failed"
)

// Ordering verdicts, in increasing severity.
const (
	OrderingOrdered  = "ordered"
	OrderingTie      = "tie"
	OrderingAfter    = "after"
	OrderingSurvivor = "survivor"
)

// Crash roles and the held manager role.
const (
	CrashUserManager = "user-manager"
	CrashBroker      = "broker"
	DaemonImage      = "winunitd.exe"
)

// HeldExit is one held old process and how it ended.
type HeldExit struct {
	Role     string `json:"role"`
	PID      uint32 `json:"pid"`
	Created  uint64 `json:"created"`
	Image    string `json:"image,omitempty"`
	SID      string `json:"sid,omitempty"`
	Exited   bool   `json:"exited"`
	ExitTime uint64 `json:"exitTime,omitempty"`
}

// Ordering compares old exits with the replacement MAIN's creation.
type Ordering struct {
	Verdict            string   `json:"verdict"`
	ReplacementCreated uint64   `json:"replacementCreated"`
	NotBefore          []string `json:"notBefore,omitempty"`
}

// ClassifyOrdering returns the most severe verdict: a survivor, an exit
// after the replacement's creation, an equal timestamp (ambiguous at the
// clock's resolution), or every old process exited strictly before.
func ClassifyOrdering(old []HeldExit, replacementCreated uint64) Ordering {
	o := Ordering{Verdict: OrderingOrdered, ReplacementCreated: replacementCreated}
	rank := map[string]int{OrderingOrdered: 0, OrderingTie: 1, OrderingAfter: 2, OrderingSurvivor: 3}
	raise := func(v string) {
		if rank[v] > rank[o.Verdict] {
			o.Verdict = v
		}
	}
	if replacementCreated == 0 || len(old) == 0 {
		o.Verdict = OrderingSurvivor
		return o
	}
	for _, h := range old {
		label := fmt.Sprintf("%s %d", h.Role, h.PID)
		switch {
		case !h.Exited || h.ExitTime == 0:
			raise(OrderingSurvivor)
			o.NotBefore = append(o.NotBefore, label+" running")
		case h.ExitTime > replacementCreated:
			raise(OrderingAfter)
			o.NotBefore = append(o.NotBefore, label+" exited after")
		case h.ExitTime == replacementCreated:
			raise(OrderingTie)
			o.NotBefore = append(o.NotBefore, label+" exited at the same instant")
		}
	}
	return o
}

// DaemonBinding fixes what one observer report is evidence of.
type DaemonBinding struct {
	Case       string `json:"case"`
	Mode       string `json:"mode"`
	Identity   string `json:"identity"`
	Repetition string `json:"repetition"`
	Source     string `json:"source"`
	Admission  string `json:"admission"`
	ExpectSID  string `json:"expectSid"`
	CrashRole  string `json:"crashRole"`
}

// ObserverReport is the observer's evidence file, rewritten at each stage.
type ObserverReport struct {
	Schema             int             `json:"schema"`
	Stage              string          `json:"stage"`
	Binding            DaemonBinding   `json:"binding"`
	Generation         int             `json:"generation"`
	Replacement        int             `json:"replacement"`
	OldTree            []Identity      `json:"oldTree,omitempty"`
	NewTree            []Identity      `json:"newTree,omitempty"`
	Crash              *HeldExit       `json:"crash,omitempty"`
	Managers           []HeldExit      `json:"managers,omitempty"`
	Old                []HeldExit      `json:"old,omitempty"`
	CrashAt            uint64          `json:"crashAt,omitempty"`
	ReplacementCreated uint64          `json:"replacementCreated,omitempty"`
	Ordering           *Ordering       `json:"ordering,omitempty"`
	EntryObservation   []PreviousCheck `json:"entryObservation,omitempty"`
	CleanupConfirmed   bool            `json:"cleanupConfirmed"`
	Failure            string          `json:"failure,omitempty"`
}

// ExpectedCrashRole is the crash target of a daemon case.
func ExpectedCrashRole(caseID string) string {
	if caseID == "N06" {
		return CrashUserManager
	}
	return CrashBroker
}

func (b DaemonBinding) validate() error {
	switch {
	case !caseIDPattern.MatchString(b.Case) || (b.Case != "N06" && b.Case != "N07"):
		return fmt.Errorf("binding case %q", b.Case)
	case !slices.Contains(LaunchModes, b.Mode):
		return fmt.Errorf("binding mode %q", b.Mode)
	case b.Identity != IdentitySystem && b.Identity != IdentityHeadless:
		return fmt.Errorf("binding identity %q", b.Identity)
	case b.Case == "N06" && b.Identity != IdentityHeadless:
		return errors.New("N06 has no SYSTEM variant")
	case !repetitionPattern.MatchString(b.Repetition):
		return fmt.Errorf("binding repetition %q", b.Repetition)
	case !fullCommit.MatchString(b.Source) || !sha256Hex.MatchString(b.Admission):
		return errors.New("binding needs the admitted source and manifest hash")
	case b.CrashRole != ExpectedCrashRole(b.Case):
		return fmt.Errorf("binding crash role %q for %s", b.CrashRole, b.Case)
	case b.Identity == IdentitySystem && b.ExpectSID != SystemSID:
		return errors.New("SYSTEM cases expect the LocalSystem SID")
	case b.Identity == IdentityHeadless && (!sidPattern.MatchString(b.ExpectSID) || b.ExpectSID == SystemSID):
		return errors.New("headless cases expect a local account SID")
	}
	return nil
}

// heldManagers is how many user managers the observer holds besides the
// crash target: a broker crash in the headless lane must also end the
// account's user manager.
func (b DaemonBinding) heldManagers() int {
	if b.CrashRole == CrashBroker && b.Identity == IdentityHeadless {
		return 1
	}
	return 0
}

// checkDaemonTree validates one generation's MAIN, ENGINE, G1 and G2 and
// that every one of them ran an admitted fixture image.
func checkDaemonTree(ids []Identity, gen int, b DaemonBinding, run *Admission) error {
	roles := []string{RoleMain, RoleEngine, RoleG1, RoleG2}
	if len(ids) != len(roles) {
		return fmt.Errorf("generation %d has %d processes", gen, len(ids))
	}
	seen := map[[2]uint64]bool{}
	for i, id := range ids {
		if id.Role != roles[i] {
			return fmt.Errorf("generation %d tree order", gen)
		}
		if err := id.validate(true); err != nil {
			return err
		}
		key := [2]uint64{uint64(id.PID), id.Created}
		if seen[key] {
			return fmt.Errorf("generation %d repeats %s", gen, id.Role)
		}
		seen[key] = true
		if id.Generation != gen {
			return fmt.Errorf("%s belongs to generation %d, want %d", id.Role, id.Generation, gen)
		}
		if id.SID != b.ExpectSID || id.Session != 0 || (b.Identity == IdentityHeadless && id.Elevated) {
			return fmt.Errorf("generation %d %s does not run as the expected account in session zero", gen, id.Role)
		}
		if !run.Admits(id.ImageSHA256) {
			return fmt.Errorf("generation %d %s image is not admitted", gen, id.Role)
		}
	}
	main, engine := ids[0], ids[1]
	if engine.ParentPID != main.PID || engine.ParentCreated != main.Created {
		return fmt.Errorf("generation %d ENGINE was not created by MAIN", gen)
	}
	for _, leaf := range ids[2:] {
		if leaf.ParentPID != engine.PID || leaf.ParentCreated != engine.Created {
			return fmt.Errorf("generation %d %s was not created by ENGINE", gen, leaf.Role)
		}
	}
	return nil
}

// ValidateDaemonReport recomputes an installed-daemon execution's proof. It
// returns nil only for a finished report bound to want whose old and new
// trees, crash target, managers, timestamps and cleanup all check out, with
// fixture images from the admitted run.
func ValidateDaemonReport(rep ObserverReport, want DaemonBinding, run *Admission) error {
	if rep.Schema != ObserverReportSchema {
		return fmt.Errorf("observer report schema %d", rep.Schema)
	}
	if err := want.validate(); err != nil {
		return err
	}
	if run == nil || run.Source != want.Source {
		return errors.New("no admitted run for this binding")
	}
	if rep.Binding != want {
		return errors.New("report is bound to another case, account or admitted run")
	}
	if rep.Stage != StageFinished || !rep.CleanupConfirmed {
		return fmt.Errorf("observer stage %s without confirmed cleanup", rep.Stage)
	}
	if rep.Generation < 1 || rep.Replacement != rep.Generation+1 {
		return fmt.Errorf("generations %d and %d are not consecutive", rep.Generation, rep.Replacement)
	}
	if err := checkDaemonTree(rep.OldTree, rep.Generation, want, run); err != nil {
		return fmt.Errorf("old tree: %w", err)
	}
	if err := checkDaemonTree(rep.NewTree, rep.Replacement, want, run); err != nil {
		return fmt.Errorf("new tree: %w", err)
	}
	for _, n := range rep.NewTree {
		for _, o := range rep.OldTree {
			if n.Same(o) {
				return errors.New("the replacement tree reuses an old process")
			}
		}
	}
	// The user manager runs as the unit's account, the broker as SYSTEM.
	crashSID := want.ExpectSID
	if want.CrashRole == CrashBroker {
		crashSID = SystemSID
	}
	if rep.Crash == nil || rep.Crash.Role != want.CrashRole || !strings.EqualFold(rep.Crash.Image, DaemonImage) ||
		rep.Crash.PID == 0 || rep.Crash.Created == 0 || rep.Crash.SID != crashSID {
		return errors.New("the crash target is not the expected daemon process")
	}
	if len(rep.Managers) != want.heldManagers() {
		return fmt.Errorf("%d held managers, want %d", len(rep.Managers), want.heldManagers())
	}
	for _, m := range rep.Managers {
		if m.Role != CrashUserManager || !strings.EqualFold(m.Image, DaemonImage) || m.PID == 0 || m.Created == 0 || m.SID != want.ExpectSID {
			return errors.New("a held manager is not the account's user-manager daemon process")
		}
	}
	// Old holds exactly the old tree, the crash target and the managers.
	expected := map[[2]uint64]string{}
	for _, id := range rep.OldTree {
		expected[[2]uint64{uint64(id.PID), id.Created}] = id.Role
	}
	expected[[2]uint64{uint64(rep.Crash.PID), rep.Crash.Created}] = rep.Crash.Role
	for _, m := range rep.Managers {
		expected[[2]uint64{uint64(m.PID), m.Created}] = m.Role
	}
	if len(rep.Old) != len(expected) {
		return fmt.Errorf("%d old exits recorded, want %d", len(rep.Old), len(expected))
	}
	var crashExit uint64
	for _, h := range rep.Old {
		key := [2]uint64{uint64(h.PID), h.Created}
		role, ok := expected[key]
		if !ok || role != h.Role {
			return fmt.Errorf("unexpected old process %s %d", h.Role, h.PID)
		}
		delete(expected, key)
		if !h.Exited || h.ExitTime == 0 || h.ExitTime < h.Created {
			return fmt.Errorf("old %s %d has no exit time", h.Role, h.PID)
		}
		if h.PID == rep.Crash.PID && h.Created == rep.Crash.Created {
			if h != *rep.Crash {
				return errors.New("the crash target's exit record is inconsistent")
			}
			crashExit = h.ExitTime
		}
	}
	if rep.CrashAt == 0 || rep.CrashAt > crashExit {
		return errors.New("crash time does not precede the crash target's exit")
	}
	for _, m := range rep.Managers {
		if !slices.Contains(rep.Old, m) {
			return errors.New("a held manager's exit record is inconsistent")
		}
	}
	created := rep.NewTree[0].Created
	if rep.ReplacementCreated != created || created <= rep.CrashAt {
		return errors.New("replacement MAIN creation time is inconsistent")
	}
	if o := ClassifyOrdering(rep.Old, created); o.Verdict != OrderingOrdered {
		return fmt.Errorf("ordering %s: %s", o.Verdict, strings.Join(o.NotBefore, ", "))
	}
	if len(rep.EntryObservation) != len(rep.OldTree) {
		return errors.New("the replacement MAIN's entry observation is missing")
	}
	for _, p := range rep.EntryObservation {
		if p.State != PreviousExited && p.State != PreviousGone && p.State != PreviousReused {
			return fmt.Errorf("entry observation saw %s %s", p.Role, p.State)
		}
	}
	return nil
}

// ObserveConfig is a validated observe command line.
type ObserveConfig struct {
	CaseDir     string
	Generation  int
	Replacement int
	CrashPID    uint32
	HoldPIDs    []uint32
	Timeout     time.Duration
	Report      string
	FinishFile  string
	Admission   string
	Binding     DaemonBinding
}

// RecordConfig is a validated record command line: it turns a finished
// observer report and the driver's own checks into one result record.
type RecordConfig struct {
	Report       string
	Results      string
	Admission    string
	Binding      DaemonBinding
	DriverResult string
	Detail       string
}

func validatePaths(paths ...string) error {
	for _, p := range paths {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p {
			return errors.New("paths must be clean absolute paths")
		}
	}
	return nil
}

func (c ObserveConfig) validate() error {
	if err := validateCaseDir(c.CaseDir); err != nil {
		return err
	}
	if c.Generation < 1 || c.Replacement != c.Generation+1 {
		return errors.New("observe needs a generation and the next one as replacement")
	}
	if c.CrashPID == 0 {
		return errors.New("observe needs --crash-pid")
	}
	if c.Timeout < time.Second || c.Timeout > 10*time.Minute {
		return errors.New("observe timeout must be between 1s and 10m")
	}
	if err := validatePaths(c.Report, c.FinishFile, c.Admission); err != nil {
		return err
	}
	if err := c.Binding.validateUnbound(); err != nil {
		return err
	}
	if len(c.HoldPIDs) != c.Binding.heldManagers() {
		return fmt.Errorf("%s %s holds %d user managers", c.Binding.Case, c.Binding.Identity, c.Binding.heldManagers())
	}
	return nil
}

func (c RecordConfig) validate() error {
	if err := validatePaths(c.Report, c.Results, c.Admission); err != nil {
		return err
	}
	if err := c.Binding.validateUnbound(); err != nil {
		return err
	}
	if c.DriverResult != ResultPass && c.DriverResult != ResultFail {
		return fmt.Errorf("driver result %q", c.DriverResult)
	}
	if len(c.Detail) > 512 || strings.ContainsAny(c.Detail, "\r\n") {
		return errors.New("detail must be one line of at most 512 bytes")
	}
	return nil
}

// bind completes a command-line binding from an admitted run.
func (b DaemonBinding) bind(run AdmittedRun) DaemonBinding {
	if run.Manifest != nil {
		b.Source, b.Admission = run.Manifest.Source, run.Hash
	}
	return b
}

// validateUnbound validates a command-line binding before the admitted run
// supplies its source and manifest hash.
func (b DaemonBinding) validateUnbound() error {
	if b.Source != "" || b.Admission != "" {
		return errors.New("the source and manifest hash come from the admitted run")
	}
	b.Source, b.Admission = strings.Repeat("0", 40), strings.Repeat("0", 64)
	return b.validate()
}

// DaemonResult builds the result record of one installed-daemon execution
// from a validated report, the admitted run and the recording executable.
func DaemonResult(m *Matrix, c RecordConfig, rep ObserverReport, run AdmittedRun, executable string) Result {
	b := c.Binding.bind(run)
	key := ExecutionKey(b.Case, b.Mode, b.Identity, "", b.Repetition, LaneDaemon)
	r := Result{
		Schema: ResultSchema, Key: key, Kind: KindPrimary, Case: b.Case, Mode: b.Mode, Identity: b.Identity,
		Repetition: b.Repetition, Lane: LaneDaemon, Result: ResultFail, Source: b.Source, Admission: b.Admission,
		Executable: executable, Matrix: MatrixHash(), Processors: goruntime.NumCPU(),
	}
	var notes []string
	var exec *Execution
	for _, e := range m.Expand() {
		if e.Key == key {
			exec = &e
		}
	}
	if exec != nil {
		r.OwnerProof = exec.OwnerProof
	}
	err := ValidateDaemonReport(rep, b, run.Manifest)
	if err == nil {
		main := rep.NewTree[0]
		source := TokenProcess
		if b.Identity == IdentityHeadless {
			source = TokenS4U
		}
		r.Token = &TokenContext{SID: main.SID, Session: main.Session, Elevated: main.Elevated, Source: source}
		r.CleanupConfirmed = true
	}
	switch {
	case exec == nil:
		notes = append(notes, "not a required execution")
	case err != nil:
		notes = append(notes, "invalid proof: "+err.Error())
	case !run.Manifest.Admits(executable):
		notes = append(notes, "recorded by an executable outside the admitted run")
	case c.DriverResult != ResultPass:
		notes = append(notes, "driver checks failed")
	default:
		r.Result = ResultPass
		notes = append(notes, "every old process exited before the replacement MAIN was created")
	}
	if c.Detail != "" {
		notes = append(notes, c.Detail)
	}
	r.Detail = strings.Join(notes, "; ")
	return r
}

func runRecord(c RecordConfig) error {
	m, err := CaseMatrix()
	if err != nil {
		return err
	}
	run, err := LoadAdmission(c.Admission)
	if err != nil {
		return err
	}
	exe, err := ExecutableSHA256()
	if err != nil {
		return err
	}
	var rep ObserverReport
	if err := ReadJSON(c.Report, &rep); err != nil {
		return err
	}
	r := DaemonResult(m, c, rep, run, exe)
	if err := WriteResult(c.Results, r); err != nil {
		return err
	}
	if r.Result != ResultPass {
		return fmt.Errorf("%s: %s", r.Key, r.Detail)
	}
	return nil
}
