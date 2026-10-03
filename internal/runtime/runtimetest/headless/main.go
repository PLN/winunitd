package headless

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Summary exit codes: complete acceptance, any failure or incompleteness,
// and a development selection whose selected records all passed.
const (
	SummaryComplete = 0
	SummaryFailed   = 1
	SummaryPartial  = 3
)

// Roles that run only on Windows, with the workload's token or SYSTEM's.
var windowsRoles = []string{"serve", "fail", "probe-token", "probe-path", "probe-tcp", "probe-smb", "probe-efs", "pipe-serve", "probe-pipe", "observe"}

// Main runs one fixture role and returns its exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "headless-workload: missing role")
		return 2
	}
	role, rest := args[0], args[1:]
	var err error
	switch role {
	case "matrix":
		err = runMatrix(rest, stdout)
	case "summarize":
		return runSummarize(rest, stdout, stderr)
	case "record":
		err = runRecord(rest)
	case "echo-serve":
		err = runEchoServe(rest)
	case "pad-log":
		err = runPadLog(rest)
	default:
		if !slices.Contains(windowsRoles, role) {
			fmt.Fprintf(stderr, "headless-workload: unknown role %q\n", role)
			return 2
		}
		err = runWindowsRole(role, rest, stdout)
	}
	var usage *usageError
	var code exitCode
	switch {
	case errors.As(err, &code):
		return int(code)
	case errors.As(err, &usage):
		fmt.Fprintln(stderr, "headless-workload:", err)
		return 2
	case err != nil:
		fmt.Fprintln(stderr, "headless-workload:", err)
		return 1
	}
	return 0
}

// exitCode is a role's deliberate exit status, such as the failing
// workload's code 7.
type exitCode uint32

func (e exitCode) Error() string { return fmt.Sprintf("exit code %d", uint32(e)) }

type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usage(format string, args ...any) error { return &usageError{fmt.Sprintf(format, args...)} }

func newFlags(role string) *flag.FlagSet {
	fs := flag.NewFlagSet(role, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return usage("%v", err)
	}
	if fs.NArg() != 0 {
		return usage("unexpected argument %q", fs.Arg(0))
	}
	return nil
}

// absPath requires a clean absolute path.
func absPath(name, p string) error {
	if p == "" || !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return usage("--%s must be a clean absolute path", name)
	}
	return nil
}

type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }

func (l *listFlag) Set(v string) error {
	for _, item := range strings.Split(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			*l = append(*l, item)
		}
	}
	return nil
}

func selectionFlags(fs *flag.FlagSet) func() (Selection, error) {
	var ledgers, cases, accounts, phases listFlag
	fs.Var(&ledgers, "ledger", "")
	fs.Var(&cases, "case", "")
	fs.Var(&accounts, "account", "")
	fs.Var(&phases, "phase", "")
	return func() (Selection, error) {
		s := Selection{Ledgers: ledgers, Cases: cases, Accounts: accounts}
		for _, l := range ledgers {
			if l != LedgerC3 && l != LedgerB && l != LedgerH {
				return s, usage("ledger %q", l)
			}
		}
		for _, c := range cases {
			if !caseIDPattern.MatchString(c) {
				return s, usage("case %q", c)
			}
		}
		for _, p := range phases {
			n, err := strconv.Atoi(p)
			if err != nil || n < 1 || n > 9 {
				return s, usage("phase %q", p)
			}
			s.Phases = append(s.Phases, n)
		}
		return s, nil
	}
}

func runMatrix(args []string, w io.Writer) error {
	fs := newFlags("matrix")
	hash := fs.Bool("hash", false, "")
	counts := fs.Bool("counts", false, "")
	sel := selectionFlags(fs)
	if err := parse(fs, args); err != nil {
		return err
	}
	s, err := sel()
	if err != nil {
		return err
	}
	m, err := CaseMatrix()
	if err != nil {
		return err
	}
	switch {
	case *hash:
		_, err := fmt.Fprintln(w, MatrixHash())
		return err
	case *counts:
		return json.NewEncoder(w).Encode(m.Counts())
	}
	enc := json.NewEncoder(w)
	for _, e := range m.Select(s) {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	return nil
}

func runSummarize(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("summarize")
	results := fs.String("results", "", "")
	admission := fs.String("admission", "", "")
	sel := selectionFlags(fs)
	err := parse(fs, args)
	if err == nil {
		err = errors.Join(absPath("results", *results), absPath("admission", *admission))
	}
	s, selErr := sel()
	if err = errors.Join(err, selErr); err != nil {
		fmt.Fprintln(stderr, "headless-workload:", err)
		return 2
	}
	m, err := CaseMatrix()
	if err != nil {
		fmt.Fprintln(stderr, "headless-workload:", err)
		return SummaryFailed
	}
	run, err := LoadAdmission(*admission)
	if err != nil {
		fmt.Fprintln(stderr, "headless-workload:", err)
		return SummaryFailed
	}
	records, err := ReadRecords(*results)
	if err != nil {
		fmt.Fprintln(stderr, "headless-workload:", err)
		return SummaryFailed
	}
	sum := Summarize(m, records, run, s)
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(sum); err != nil {
		return SummaryFailed
	}
	switch {
	case sum.Complete:
		return SummaryComplete
	case sum.Partial && sum.Passed == sum.Selected && len(sum.Problems) == 0:
		fmt.Fprintln(stderr, "headless-workload: partial qualification; #266 acceptance stays open")
		return SummaryPartial
	}
	return SummaryFailed
}

// Observation is what a driver hands to record: everything but the run
// binding, which record adds itself.
type Observation struct {
	Key              string   `json:"key"`
	Kind             string   `json:"kind"`
	Result           string   `json:"result"`
	Token            *Token   `json:"token,omitempty"`
	ExecutionID      string   `json:"executionId"`
	Sequence         int      `json:"sequence"`
	BootID           string   `json:"bootId"`
	PasswordLogons   int      `json:"passwordLogons"`
	RunnerID         string   `json:"runnerId,omitempty"`
	CleanupConfirmed bool     `json:"cleanupConfirmed"`
	Controls         []string `json:"controls,omitempty"`
	Evidence         Evidence `json:"evidence"`
	Detail           string   `json:"detail,omitempty"`
}

var executionPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// BuildRecord binds an observation to the admitted run, the matrix and the
// recording executable. It accepts only keys of the matrix; the summary
// judges the evidence. An observed case's lifecycle evidence is the
// observer's report, passed separately and never inside the observation; a
// passing observed record needs one from an admitted observer on the
// record's boot.
func BuildRecord(m *Matrix, o Observation, report *ObserverReport, run AdmittedRun, executable string) (Record, error) {
	if run.Manifest == nil {
		return Record{}, errors.New("no admitted run")
	}
	if o.Evidence.Observer != nil {
		return Record{}, errors.New("an observation cannot carry an observer report")
	}
	entries := map[string]Entry{}
	controls := map[string]bool{}
	for _, e := range m.Expand() {
		entries[e.Key] = e
		for _, c := range e.Controls {
			controls[c.Key] = true
		}
	}
	switch o.Kind {
	case KindPrimary:
		if e, ok := entries[o.Key]; !ok || e.Plane == PlaneReference {
			return Record{}, fmt.Errorf("%s is not an executed record of this matrix", o.Key)
		}
	case KindControl:
		if !controls[o.Key] {
			return Record{}, fmt.Errorf("%s is not a control of this matrix", o.Key)
		}
	default:
		return Record{}, fmt.Errorf("kind %q", o.Kind)
	}
	switch o.Result {
	case ResultPass, ResultFail, ResultSkip, ResultInconclusive:
	default:
		return Record{}, fmt.Errorf("result %q", o.Result)
	}
	if o.Kind == KindPrimary && !executionPattern.MatchString(o.ExecutionID) || o.Sequence < 1 || !executionPattern.MatchString(o.BootID) {
		return Record{}, errors.New("an execution, a sequence and a boot are required")
	}
	if len(o.Detail) > 512 || strings.ContainsAny(o.Detail, "\r\n") {
		return Record{}, errors.New("detail must be one line of at most 512 bytes")
	}
	observed := o.Kind == KindPrimary && entries[o.Key].Observe != nil
	switch {
	case report != nil && !observed:
		return Record{}, fmt.Errorf("%s is not an observed case", o.Key)
	case report == nil && observed && o.Result == ResultPass:
		return Record{}, fmt.Errorf("a passing %s record needs the observer's report", o.Key)
	case report != nil:
		if err := report.Validate(); err != nil {
			return Record{}, fmt.Errorf("observer report: %w", err)
		}
		if !run.Manifest.Admits(report.Executable) {
			return Record{}, errors.New("the observer is not in the admitted run manifest")
		}
		if report.Boot.String() != o.BootID {
			return Record{}, errors.New("the observer report is from another boot")
		}
		o.Evidence.Observer = report
	}
	return Record{
		Schema: RecordSchema, Key: o.Key, Kind: o.Kind, Result: o.Result, Source: run.Manifest.Source, Admission: run.Hash,
		Executable: executable, Matrix: MatrixHash(), Token: o.Token, ExecutionID: o.ExecutionID, Sequence: o.Sequence,
		BootID: o.BootID, PasswordLogons: o.PasswordLogons, RunnerID: o.RunnerID, CleanupConfirmed: o.CleanupConfirmed,
		Controls: o.Controls, Evidence: o.Evidence, Detail: o.Detail,
	}, nil
}

func runRecord(args []string) error {
	fs := newFlags("record")
	observation := fs.String("observation", "", "")
	observer := fs.String("observer", "", "")
	results := fs.String("results", "", "")
	admission := fs.String("admission", "", "")
	if err := parse(fs, args); err != nil {
		return err
	}
	if err := errors.Join(absPath("observation", *observation), absPath("results", *results), absPath("admission", *admission)); err != nil {
		return err
	}
	var report *ObserverReport
	if *observer != "" {
		if err := absPath("observer", *observer); err != nil {
			return err
		}
		data, err := readBounded(*observer)
		if err != nil {
			return err
		}
		report = &ObserverReport{}
		if err := decodeStrict(data, report); err != nil {
			return fmt.Errorf("observer report: %w", err)
		}
	}
	m, err := CaseMatrix()
	if err != nil {
		return err
	}
	run, err := LoadAdmission(*admission)
	if err != nil {
		return err
	}
	exe, err := ExecutableSHA256()
	if err != nil {
		return err
	}
	if !run.Manifest.Admits(exe) {
		return errors.New("this executable is not in the admitted run manifest")
	}
	data, err := readBounded(*observation)
	if err != nil {
		return err
	}
	var o Observation
	if err := decodeStrict(data, &o); err != nil {
		return fmt.Errorf("observation: %w", err)
	}
	r, err := BuildRecord(m, o, report, run, exe)
	if err != nil {
		return err
	}
	return WriteRecord(*results, r)
}

// durationFlag bounds a duration flag.
func durationIn(name string, d, lo, hi time.Duration) error {
	if d < lo || d > hi {
		return usage("--%s must be between %s and %s", name, lo, hi)
	}
	return nil
}
