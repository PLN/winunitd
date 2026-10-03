package nestedjob

import (
	"errors"
	"os"
	"regexp"
	"strings"
)

// Environment of a native-owner qualification run. The lab driver sets these
// for selected test invocations; an ordinary test run sets none of them.
const (
	// EnvResults is an absolute directory that receives one result record
	// per scenario. Unset: no record is written.
	EnvResults = "WINUNITD_NATIVE_NESTED_RESULTS"
	// EnvSource is the admitted source commit the binaries were built from.
	EnvSource = "WINUNITD_NATIVE_NESTED_SOURCE"
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

// Record collects one native-owner scenario's evidence.
type Record struct {
	Exec             Execution
	Token            *TokenContext
	CleanupConfirmed bool
	Controls         []Result
	notes            []string
	cleanupSeen      bool
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

// Note adds a bounded line to the record's detail.
func (r *Record) Note(s string) {
	if len(r.notes) < 32 {
		r.notes = append(r.notes, s)
	}
}

// Control adds a separately keyed control result, such as an uncapped CPU
// control, and links it to the primary record.
func (r *Record) Control(name, result, detail string) {
	c := r.result(result)
	c.Kind = KindControl
	c.Key = r.Exec.Key + "#" + name
	c.Detail = detail
	r.Controls = append(r.Controls, c)
}

func (r *Record) result(result string) Result {
	return Result{
		Schema: ResultSchema, Key: r.Exec.Key, Kind: KindPrimary, Case: r.Exec.Case, Mode: r.Exec.Mode,
		Identity: r.Exec.Identity, Phase: r.Exec.Phase, Repetition: r.Exec.Repetition, Lane: r.Exec.Lane,
		Result: result, Source: os.Getenv(EnvSource), Matrix: MatrixHash(), Token: r.Token,
		CleanupConfirmed: r.CleanupConfirmed,
	}
}

// Finish writes the primary record and its controls when EnvResults is set.
func (r *Record) Finish(result string) error {
	dir := os.Getenv(EnvResults)
	if dir == "" {
		return nil
	}
	primary := r.result(result)
	primary.Detail = strings.Join(r.notes, "; ")
	var errs []error
	for _, c := range r.Controls {
		primary.Controls = append(primary.Controls, c.Key)
		errs = append(errs, WriteResult(dir, c))
	}
	errs = append(errs, WriteResult(dir, primary))
	return errors.Join(errs...)
}
