package nestedjob

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"strings"
)

// Environment of a native-owner qualification run. The lab driver sets these
// for selected test invocations; an ordinary test run sets none of them.
const (
	// EnvResults is an absolute directory that receives one result record
	// per scenario. Unset: no record is written.
	EnvResults = "WINUNITD_NATIVE_NESTED_RESULTS"
	// EnvRepetition labels this execution r1..r9; unset means r1.
	EnvRepetition = "WINUNITD_NATIVE_NESTED_REPETITION"
	// EnvCaseRoot is an absolute directory that keeps case directories as
	// evidence. Unset: cases use test temporary directories.
	EnvCaseRoot = "WINUNITD_NATIVE_NESTED_CASE_ROOT"
	// EnvHeadlessSID selects the headless lane's dedicated, logged-off local
	// standard account. Unset: headless scenarios skip.
	EnvHeadlessSID = "WINUNITD_NATIVE_NESTED_HEADLESS_SID"
	// EnvFixture must be "disposable" whenever EnvHeadlessSID is set.
	EnvFixture = "WINUNITD_NATIVE_NESTED_FIXTURE"
	// EnvMeter enables the CPU quota measurement.
	EnvMeter = "WINUNITD_NATIVE_NESTED_METER"
)

var sidPattern = regexp.MustCompile(`^S-1-5-21-[0-9]+-[0-9]+-[0-9]+-[0-9]+$`)

// HeadlessSID returns the configured headless account SID, "" when the lane
// is not configured, or an error for an unusable configuration.
func HeadlessSID() (string, error) {
	sid := os.Getenv(EnvHeadlessSID)
	if sid == "" {
		return "", nil
	}
	if os.Getenv(EnvFixture) != "disposable" {
		return "", errors.New(EnvFixture + "=disposable is required with a headless account")
	}
	if !sidPattern.MatchString(sid) {
		return "", errors.New("headless account must be a local account SID")
	}
	return sid, nil
}

// Repetition returns the configured repetition label.
func Repetition() (string, error) {
	rep := os.Getenv(EnvRepetition)
	if rep == "" {
		return "r1", nil
	}
	if !repetitionPattern.MatchString(rep) {
		return "", errors.New("repetition must be r1 to r9")
	}
	return rep, nil
}

// Record collects one native-owner scenario's evidence. Its result is
// bound to the admitted run at Finish, never to a label the runner chose.
type Record struct {
	Exec             Execution
	Token            *TokenContext
	CleanupConfirmed bool
	notes            []string
	cleanupSeen      bool
	inconclusive     []string
	controls         []*control
}

// control is one separately keyed control execution of a scenario, with
// its own owner token and its own cleanup.
type control struct {
	name        string
	result      string
	detail      string
	token       *TokenContext
	cleanup     bool
	cleanupSeen bool
}

// Cleanup records one owned resource's final cleanup. A scenario that starts
// several units is confirmed only when every one of them was.
func (r *Record) Cleanup(confirmed bool) {
	if !r.cleanupSeen {
		r.cleanupSeen = true
		r.CleanupConfirmed = confirmed
		return
	}
	r.CleanupConfirmed = r.CleanupConfirmed && confirmed
}

// NewRecord starts the record of one native-owner scenario.
func NewRecord(caseID, mode, identity, phase string) (*Record, error) {
	rep, err := Repetition()
	if err != nil {
		return nil, err
	}
	e := Execution{Case: caseID, Mode: mode, Identity: identity, Phase: phase, Repetition: rep, Lane: LaneOwner}
	e.Key = ExecutionKey(caseID, mode, identity, phase, rep, LaneOwner)
	return &Record{Exec: e}, nil
}

// Qualifying reports whether this run writes result records. Settings-only
// shortcuts that suit an ordinary regression run must not pass then.
func Qualifying() bool { return os.Getenv(EnvResults) != "" }

// Note adds a bounded line to the record's detail.
func (r *Record) Note(s string) {
	if len(r.notes) < 32 {
		r.notes = append(r.notes, s)
	}
}

// MarkInconclusive keeps a scenario that met every assertion it could make
// from passing: its evidence is insufficient for the case's claim.
func (r *Record) MarkInconclusive(reason string) {
	r.inconclusive = append(r.inconclusive, reason)
}

func (r *Record) control(name string) *control {
	for _, c := range r.controls {
		if c.name == name {
			return c
		}
	}
	c := &control{name: name, result: ResultFail, detail: "control did not report"}
	r.controls = append(r.controls, c)
	return c
}

// Control sets a separately keyed control's result, such as an uncapped
// CPU control, under the token that ran it. The control's cleanup is
// recorded separately with ControlCleanup when it is confirmed.
func (r *Record) Control(name, result, detail string, token *TokenContext) {
	c := r.control(name)
	c.result, c.detail, c.token = result, detail, token
}

// ControlCleanup records one of a control's owned resources' final cleanup.
func (r *Record) ControlCleanup(name string, confirmed bool) {
	c := r.control(name)
	c.cleanup = confirmed && (c.cleanup || !c.cleanupSeen)
	c.cleanupSeen = true
}

// admitted returns the admitted run named by EnvAdmission and the running
// executable's hash.
func admitted() (AdmittedRun, string, error) {
	path := os.Getenv(EnvAdmission)
	if path == "" || !filepath.IsAbs(path) {
		return AdmittedRun{}, "", errors.New(EnvAdmission + " must name the admitted run manifest")
	}
	run, err := LoadAdmission(path)
	if err != nil {
		return AdmittedRun{}, "", err
	}
	exe, err := ExecutableSHA256()
	if err != nil {
		return run, "", err
	}
	if !run.Manifest.Admits(exe) {
		return run, exe, errors.New("this executable is not in the admitted run manifest")
	}
	return run, exe, nil
}

// Finish writes the primary record and its controls when EnvResults is set.
// A run without a loadable admission manifest, or from an executable it
// does not admit, still writes its evidence but cannot pass.
func (r *Record) Finish(result string) error {
	dir := os.Getenv(EnvResults)
	if dir == "" {
		return nil
	}
	run, exe, admErr := admitted()
	if admErr != nil {
		r.MarkInconclusive("not an admitted run")
	}
	bind := func(res Result) Result {
		if run.Manifest != nil {
			res.Source, res.Admission = run.Manifest.Source, run.Hash
		}
		res.Executable, res.Matrix, res.Processors = exe, MatrixHash(), goruntime.NumCPU()
		return res
	}
	primary := bind(Result{
		Schema: ResultSchema, Key: r.Exec.Key, Kind: KindPrimary, Case: r.Exec.Case, Mode: r.Exec.Mode,
		Identity: r.Exec.Identity, Phase: r.Exec.Phase, Repetition: r.Exec.Repetition, Lane: r.Exec.Lane,
		Result: result, Token: r.Token, CleanupConfirmed: r.CleanupConfirmed,
	})
	notes := r.notes
	if result == ResultPass && len(r.inconclusive) > 0 {
		primary.Result = ResultInconclusive
	}
	for _, reason := range r.inconclusive {
		notes = append(notes, "inconclusive: "+reason)
	}
	primary.Detail = strings.Join(notes, "; ")
	var errs []error
	for _, c := range r.controls {
		res := primary
		res.Kind, res.Key, res.Result, res.Detail = KindControl, r.Exec.Key+"#"+c.name, c.result, c.detail
		res.Token, res.CleanupConfirmed, res.Controls = c.token, c.cleanupSeen && c.cleanup, nil
		if admErr != nil && res.Result == ResultPass {
			res.Result = ResultInconclusive
		}
		primary.Controls = append(primary.Controls, res.Key)
		errs = append(errs, WriteResult(dir, res))
	}
	errs = append(errs, WriteResult(dir, primary))
	if admErr != nil {
		errs = append(errs, fmt.Errorf("result record: %w", admErr))
	}
	return errors.Join(errs...)
}

// WriteSupplementary records a supplementary regression under its own key
// when EnvResults is set, bound to the admitted run like every record.
func WriteSupplementary(key, result string, cleanup bool, token *TokenContext) error {
	dir := os.Getenv(EnvResults)
	if dir == "" {
		return nil
	}
	run, exe, admErr := admitted()
	r := Result{Schema: ResultSchema, Key: key, Kind: KindSupplementary, Result: result, Executable: exe,
		Matrix: MatrixHash(), Processors: goruntime.NumCPU(), Token: token, CleanupConfirmed: cleanup}
	if run.Manifest != nil {
		r.Source, r.Admission = run.Manifest.Source, run.Hash
	}
	if admErr != nil {
		if r.Result == ResultPass {
			r.Result = ResultInconclusive
		}
		admErr = fmt.Errorf("supplementary record: %w", admErr)
	}
	return errors.Join(WriteResult(dir, r), admErr)
}
