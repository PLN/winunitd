package nestedjob

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Installed-daemon lane (N06, N07). The observer holds the old tree and any
// named manager processes, terminates the exact crash target, waits for the
// replacement generation and compares kernel exit times of every old process
// with the replacement MAIN's creation time. That ordering, not a later poll
// or MAIN's own entry check, supports "gone before replacement". Entry checks
// are reported separately as entry observations.

// ObserverReportSchema is the observer report schema.
const ObserverReportSchema = 1

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

// HeldExit is one held old process and how it ended.
type HeldExit struct {
	Role     string `json:"role"`
	PID      uint32 `json:"pid"`
	Created  uint64 `json:"created"`
	Image    string `json:"image,omitempty"`
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

// ObserverReport is the observer's evidence file, rewritten at each stage.
type ObserverReport struct {
	Schema           int             `json:"schema"`
	Stage            string          `json:"stage"`
	Generation       int             `json:"generation"`
	Replacement      int             `json:"replacement"`
	Old              []HeldExit      `json:"old"`
	Crash            *HeldExit       `json:"crash,omitempty"`
	CrashAt          uint64          `json:"crashAt,omitempty"`
	New              []Identity      `json:"new,omitempty"`
	Ordering         *Ordering       `json:"ordering,omitempty"`
	EntryObservation []PreviousCheck `json:"entryObservation,omitempty"`
	CleanupConfirmed bool            `json:"cleanupConfirmed"`
	Failure          string          `json:"failure,omitempty"`
}

// ObserveConfig is a validated observe command line.
type ObserveConfig struct {
	CaseDir     string
	Generation  int
	Replacement int
	CrashPID    uint32
	CrashImage  string
	HoldPIDs    []uint32
	Timeout     time.Duration
	Report      string
	FinishFile  string
}

// RecordConfig is a validated record command line: it turns a finished
// observer report and the driver's own checks into one result record.
type RecordConfig struct {
	Report       string
	Results      string
	Case         string
	Mode         string
	Identity     string
	Repetition   string
	Source       string
	TokenSource  string
	DriverResult string
	Detail       string
}

var imageName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}\.exe$`)

func (c ObserveConfig) validate() error {
	if err := validateCaseDir(c.CaseDir); err != nil {
		return err
	}
	if c.Generation < 1 || c.Replacement <= c.Generation || c.Replacement > MaxGeneration {
		return errors.New("observe needs 1 <= generation < replacement")
	}
	if c.CrashPID == 0 || !imageName.MatchString(c.CrashImage) {
		return errors.New("observe needs --crash-pid and an executable --crash-image name")
	}
	if c.Timeout < time.Second || c.Timeout > 10*time.Minute {
		return errors.New("observe timeout must be between 1s and 10m")
	}
	if len(c.HoldPIDs) > 8 {
		return errors.New("observe holds at most 8 extra processes")
	}
	for _, p := range []string{c.Report, c.FinishFile} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p {
			return errors.New("observe report and finish paths must be clean absolute paths")
		}
	}
	return nil
}

func (c RecordConfig) validate() error {
	if !filepath.IsAbs(c.Report) || !filepath.IsAbs(c.Results) {
		return errors.New("record needs absolute --report and --results")
	}
	if !caseIDPattern.MatchString(c.Case) || !repetitionPattern.MatchString(c.Repetition) || !sourceCommit.MatchString(c.Source) {
		return errors.New("record needs --case, --repetition and --source")
	}
	if c.TokenSource != TokenProcess && c.TokenSource != TokenS4U {
		return fmt.Errorf("token source %q", c.TokenSource)
	}
	if c.DriverResult != ResultPass && c.DriverResult != ResultFail {
		return fmt.Errorf("driver result %q", c.DriverResult)
	}
	if len(c.Detail) > 512 || strings.ContainsAny(c.Detail, "\r\n") {
		return errors.New("detail must be one line of at most 512 bytes")
	}
	return nil
}

// DaemonResult builds the result record of one installed-daemon execution.
// It passes only when the matrix requires the execution, the observer
// finished with every old process exited strictly before the replacement
// MAIN was created and with confirmed cleanup, the replacement tree ran with
// one token supporting the identity, and the driver's own checks passed.
func DaemonResult(m *Matrix, c RecordConfig, rep ObserverReport) Result {
	key := ExecutionKey(c.Case, c.Mode, c.Identity, "", c.Repetition, LaneDaemon)
	r := Result{
		Schema: ResultSchema, Key: key, Kind: KindPrimary, Case: c.Case, Mode: c.Mode, Identity: c.Identity,
		Repetition: c.Repetition, Lane: LaneDaemon, Result: ResultFail, Source: c.Source, Matrix: MatrixHash(),
		CleanupConfirmed: rep.Stage == StageFinished && rep.CleanupConfirmed,
	}
	var notes []string
	var exec *Execution
	for _, e := range m.Expand() {
		if e.Key == key {
			exec = &e
		}
	}
	if exec == nil {
		notes = append(notes, "not a required execution")
	} else {
		r.OwnerProof = exec.OwnerProof
	}
	if len(rep.New) > 0 {
		first := rep.New[0]
		r.Token = &TokenContext{SID: first.SID, Session: first.Session, Elevated: first.Elevated, Source: c.TokenSource}
		for _, id := range rep.New[1:] {
			if id.SID != first.SID || id.Session != first.Session || id.Elevated != first.Elevated {
				notes = append(notes, "replacement tree mixes tokens")
				r.Token = nil
				break
			}
		}
	}
	switch {
	case rep.Schema != ObserverReportSchema:
		notes = append(notes, "observer report schema")
	case rep.Stage != StageFinished:
		notes = append(notes, "observer stage "+rep.Stage+": "+rep.Failure)
	case rep.Ordering == nil || rep.Ordering.Verdict != OrderingOrdered:
		notes = append(notes, "old processes were not all gone before the replacement")
	case !rep.CleanupConfirmed:
		notes = append(notes, "cleanup not confirmed")
	case IdentityOf(r.Token) != c.Identity:
		notes = append(notes, "replacement token does not support "+c.Identity)
	case c.DriverResult != ResultPass:
		notes = append(notes, "driver checks failed")
	case exec != nil:
		r.Result = ResultPass
	}
	if rep.Ordering != nil {
		notes = append(notes, "ordering "+rep.Ordering.Verdict)
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
	var rep ObserverReport
	if err := ReadJSON(c.Report, &rep); err != nil {
		return err
	}
	r := DaemonResult(m, c, rep)
	if err := WriteResult(c.Results, r); err != nil {
		return err
	}
	if r.Result != ResultPass {
		return fmt.Errorf("%s: %s", r.Key, r.Detail)
	}
	return nil
}
