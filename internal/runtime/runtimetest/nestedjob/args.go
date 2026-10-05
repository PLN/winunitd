// Package nestedjob is the test-only workload fixture for issue #265. A unit
// main process (MAIN) creates its own unnamed kill-on-close Job Object and
// places an engine (ENGINE) in it, either by suspended assignment or
// atomically with PROC_THREAD_ATTRIBUTE_JOB_LIST. ENGINE starts two ordinary
// grandchildren (G1, G2). MAIN stays outside its inner job so it can report
// what happens when that job closes.
//
// Native test binaries route their -winunitd-helper=nested-job selector to
// Main, and tests/native/nested-job wraps it as a standalone executable for
// installed-daemon qualification. It is not a release payload and contains
// no consumer engine, lease or authentication logic.
package nestedjob

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Launch modes. There is no automatic fallback between them.
const (
	ModeAssign  = "assign"
	ModeJobList = "job-list"
)

// LaunchModes lists every launch mode in matrix order.
var LaunchModes = []string{ModeAssign, ModeJobList}

// Process roles.
const (
	RoleMain   = "main"
	RoleEngine = "engine"
	RoleG1     = "g1"
	RoleG2     = "g2"
	RoleProbe  = "probe"
	RoleStop   = "stop"
	// RoleOwner is a native-owner agent: it owns the unit job of a scenario
	// that runs under another identity, such as a genuine S4U process.
	RoleOwner = "owner"
	// RoleManagerOwner runs an isolated Manager for one nested unit under
	// another identity and answers enumerated questions about it.
	RoleManagerOwner = "manager-owner"
)

// Work selects what ENGINE, G1 and G2 do after the observer's start-work
// command. Nothing allocates or spins before that acknowledgment.
const (
	WorkIdle   = "idle"
	WorkCPU    = "cpu"
	WorkCommit = "commit"
)

// MAIN's response to a cooperative stop request.
const (
	OnStopCooperative = "cooperative"
	OnStopIgnore      = "ignore"
)

// ExecStop helper behaviors.
const (
	StopCooperative = "cooperative"
	StopHang        = "hang"
)

// Launch gates hold MAIN after creating ENGINE so a test can kill it there.
const (
	GatePreAssign = "pre-assign"
	GatePreResume = "pre-resume"
)

// SensitivityInheritInner deliberately passes one inheritable duplicate of
// the inner job to ENGINE. It is the positive control for the inheritance
// probe and the last-handle lifetime, never a safe production shape.
const SensitivityInheritInner = "inherit-inner"

// MaxGeneration bounds the four-digit generation directory names.
const MaxGeneration = 9999

// MainConfig is a validated main-role command line.
type MainConfig struct {
	LaunchMode  string
	CaseDir     string
	Generation  int // 0 selects the next generation after the previous manifest
	Work        string
	OnStop      string
	Gate        string
	Sensitivity string
}

// EngineConfig is a validated engine-role command line.
type EngineConfig struct {
	CaseDir    string
	Generation int
	JobHandle  uint64 // numeric value probed for an inherited job handle
	Work       string
}

// LeafConfig is a validated leaf-role command line (G1, G2 or a probe).
type LeafConfig struct {
	CaseDir    string
	Generation int
	Role       string
	JobHandle  uint64
	Work       string
}

// StopConfig is a validated ExecStop helper command line.
type StopConfig struct {
	CaseDir  string
	Behavior string
}

// MatrixConfig selects expanded matrix executions to print, or the matrix
// hash.
type MatrixConfig struct {
	Select Selection
	Hash   bool
}

// SummarizeConfig evaluates a result directory for one admitted run
// manifest.
type SummarizeConfig struct {
	Results   string
	Admission string
	Select    Selection
}

// Invocation is one parsed fixture command line. Exactly one role field is set.
type Invocation struct {
	Role      string
	Main      *MainConfig
	Engine    *EngineConfig
	Leaf      *LeafConfig
	Stop      *StopConfig
	Matrix    *MatrixConfig
	Summarize *SummarizeConfig
	Observe   *ObserveConfig
	Record    *RecordConfig
}

// Parse validates a fixture command line: the role followed by its flags.
// Unknown roles, flags, positional arguments and enum values are errors.
func Parse(args []string) (Invocation, error) {
	if len(args) == 0 {
		return Invocation{}, errors.New("missing role")
	}
	role, rest := args[0], args[1:]
	fs := flag.NewFlagSet(role, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	caseDir := fs.String("case-dir", "", "")
	switch role {
	case RoleMain:
		mode := fs.String("launch-mode", "", "")
		gen := fs.String("generation", "", "")
		work := fs.String("engine-mode", WorkIdle, "")
		onStop := fs.String("on-stop", OnStopCooperative, "")
		gate := fs.String("hold-gate", "", "")
		sensitivity := fs.String("sensitivity", "", "")
		if err := parseFlags(fs, rest); err != nil {
			return Invocation{}, err
		}
		c := &MainConfig{LaunchMode: *mode, CaseDir: *caseDir, Work: *work, OnStop: *onStop, Gate: *gate, Sensitivity: *sensitivity}
		n, err := parseGeneration(*gen, true)
		if err != nil {
			return Invocation{}, err
		}
		c.Generation = n
		if err := c.validate(); err != nil {
			return Invocation{}, err
		}
		return Invocation{Role: role, Main: c}, nil
	case RoleEngine:
		gen := fs.String("generation", "", "")
		handle := fs.Uint64("job-handle-number", 0, "")
		work := fs.String("work", WorkIdle, "")
		if err := parseFlags(fs, rest); err != nil {
			return Invocation{}, err
		}
		n, err := parseGeneration(*gen, false)
		if err != nil {
			return Invocation{}, err
		}
		c := &EngineConfig{CaseDir: *caseDir, Generation: n, JobHandle: *handle, Work: *work}
		if err := validateCommon(c.CaseDir, c.Work, c.JobHandle); err != nil {
			return Invocation{}, err
		}
		return Invocation{Role: role, Engine: c}, nil
	case "leaf":
		gen := fs.String("generation", "", "")
		leafRole := fs.String("role", "", "")
		handle := fs.Uint64("job-handle-number", 0, "")
		work := fs.String("work", WorkIdle, "")
		if err := parseFlags(fs, rest); err != nil {
			return Invocation{}, err
		}
		n, err := parseGeneration(*gen, false)
		if err != nil {
			return Invocation{}, err
		}
		c := &LeafConfig{CaseDir: *caseDir, Generation: n, Role: *leafRole, JobHandle: *handle, Work: *work}
		switch c.Role {
		case RoleG1, RoleG2, RoleProbe:
		default:
			return Invocation{}, fmt.Errorf("invalid leaf role %q", c.Role)
		}
		if err := validateCommon(c.CaseDir, c.Work, c.JobHandle); err != nil {
			return Invocation{}, err
		}
		return Invocation{Role: "leaf", Leaf: c}, nil
	case RoleStop:
		behavior := fs.String("behavior", "", "")
		if err := parseFlags(fs, rest); err != nil {
			return Invocation{}, err
		}
		c := &StopConfig{CaseDir: *caseDir, Behavior: *behavior}
		if err := validateCaseDir(c.CaseDir); err != nil {
			return Invocation{}, err
		}
		if c.Behavior != StopCooperative && c.Behavior != StopHang {
			return Invocation{}, fmt.Errorf("invalid stop behavior %q", c.Behavior)
		}
		return Invocation{Role: role, Stop: c}, nil
	case "observe":
		c := &ObserveConfig{}
		gen := fs.Int("generation", 0, "")
		crashPID := fs.Uint64("crash-pid", 0, "")
		var holds listFlag
		fs.Var(&holds, "hold-pid", "")
		fs.DurationVar(&c.Timeout, "timeout", 3*time.Minute, "")
		fs.StringVar(&c.Report, "report", "", "")
		fs.StringVar(&c.FinishFile, "finish-file", "", "")
		fs.StringVar(&c.Admission, "admission", "", "")
		bindingFlags(fs, &c.Binding)
		if err := parseFlags(fs, rest); err != nil {
			return Invocation{}, err
		}
		c.CaseDir, c.Generation, c.Replacement = *caseDir, *gen, *gen+1
		c.Binding.CrashRole = ExpectedCrashRole(c.Binding.Case)
		if *crashPID > 0 && *crashPID <= 1<<32-1 {
			c.CrashPID = uint32(*crashPID)
		}
		for _, h := range holds {
			pid, err := strconv.ParseUint(h, 10, 32)
			if err != nil || pid == 0 {
				return Invocation{}, fmt.Errorf("invalid hold pid %q", h)
			}
			c.HoldPIDs = append(c.HoldPIDs, uint32(pid))
		}
		if err := c.validate(); err != nil {
			return Invocation{}, err
		}
		return Invocation{Role: role, Observe: c}, nil
	case "record":
		c := &RecordConfig{}
		fs.StringVar(&c.Report, "report", "", "")
		fs.StringVar(&c.Results, "results", "", "")
		fs.StringVar(&c.Admission, "admission", "", "")
		fs.StringVar(&c.DriverResult, "driver-result", "", "")
		fs.StringVar(&c.Detail, "detail", "", "")
		bindingFlags(fs, &c.Binding)
		if err := parseFlags(fs, rest); err != nil {
			return Invocation{}, err
		}
		if *caseDir != "" {
			return Invocation{}, errors.New("record takes no case directory")
		}
		c.Binding.CrashRole = ExpectedCrashRole(c.Binding.Case)
		if err := c.validate(); err != nil {
			return Invocation{}, err
		}
		return Invocation{Role: role, Record: c}, nil
	case "matrix", "summarize":
		var lanes, identities, cases listFlag
		fs.Var(&lanes, "lane", "")
		fs.Var(&identities, "identity", "")
		fs.Var(&cases, "case", "")
		first := fs.Bool("first-increment", false, "")
		hash := fs.Bool("hash", false, "")
		results := fs.String("results", "", "")
		admission := fs.String("admission", "", "")
		if err := parseFlags(fs, rest); err != nil {
			return Invocation{}, err
		}
		if *caseDir != "" {
			return Invocation{}, fmt.Errorf("%s takes no case directory", role)
		}
		sel := Selection{Lanes: lanes, Identities: identities, Cases: cases, FirstIncrement: *first}
		for _, v := range sel.Lanes {
			if v != LaneOwner && v != LaneDaemon {
				return Invocation{}, fmt.Errorf("invalid lane %q", v)
			}
		}
		for _, v := range sel.Identities {
			if v != IdentitySystem && v != IdentityHeadless {
				return Invocation{}, fmt.Errorf("invalid identity %q", v)
			}
		}
		for _, v := range sel.Cases {
			if !caseIDPattern.MatchString(v) {
				return Invocation{}, fmt.Errorf("invalid case %q", v)
			}
		}
		if role == "matrix" {
			if *results != "" || *admission != "" {
				return Invocation{}, errors.New("matrix takes no results or admission")
			}
			if *hash && !sel.Empty() {
				return Invocation{}, errors.New("the matrix hash covers the whole matrix")
			}
			return Invocation{Role: role, Matrix: &MatrixConfig{Select: sel, Hash: *hash}}, nil
		}
		if *hash || validatePaths(*results, *admission) != nil {
			return Invocation{}, errors.New("summarize needs --results ABSOLUTE-DIR and --admission ABSOLUTE-FILE")
		}
		return Invocation{Role: role, Summarize: &SummarizeConfig{Results: *results, Admission: *admission, Select: sel}}, nil
	default:
		return Invocation{}, fmt.Errorf("unknown role %q", role)
	}
}

// listFlag accepts repeated or comma-separated values.
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

// bindingFlags registers the scenario a daemon report is evidence of. The
// crash role follows from the case; the source and manifest hash come from
// the admitted run manifest, never from the command line.
func bindingFlags(fs *flag.FlagSet, b *DaemonBinding) {
	fs.StringVar(&b.Case, "case", "", "")
	fs.StringVar(&b.Mode, "mode", "", "")
	fs.StringVar(&b.Identity, "identity", "", "")
	fs.StringVar(&b.Repetition, "repetition", "", "")
	fs.StringVar(&b.ExpectSID, "expect-sid", "", "")
}

func parseFlags(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	return nil
}

func parseGeneration(v string, allowAuto bool) (int, error) {
	if v == "auto" && allowAuto {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > MaxGeneration || strconv.Itoa(n) != v {
		return 0, fmt.Errorf("invalid generation %q", v)
	}
	return n, nil
}

func validateCaseDir(dir string) error {
	if dir == "" || !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return fmt.Errorf("case directory must be a clean absolute path: %q", dir)
	}
	return nil
}

func validateCommon(dir, work string, handle uint64) error {
	if err := validateCaseDir(dir); err != nil {
		return err
	}
	switch work {
	case WorkIdle, WorkCPU, WorkCommit:
	default:
		return fmt.Errorf("invalid work %q", work)
	}
	if handle == 0 {
		return errors.New("job-handle-number is required for the inheritance probe")
	}
	return nil
}

func (c *MainConfig) validate() error {
	if c.LaunchMode != ModeAssign && c.LaunchMode != ModeJobList {
		return fmt.Errorf("invalid launch mode %q", c.LaunchMode)
	}
	if err := validateCaseDir(c.CaseDir); err != nil {
		return err
	}
	switch c.Work {
	case WorkIdle, WorkCPU, WorkCommit:
	default:
		return fmt.Errorf("invalid engine mode %q", c.Work)
	}
	if c.OnStop != OnStopCooperative && c.OnStop != OnStopIgnore {
		return fmt.Errorf("invalid on-stop %q", c.OnStop)
	}
	switch c.Gate {
	case "":
	case GatePreAssign:
		if c.LaunchMode != ModeAssign {
			return errors.New("pre-assign gate exists only in assign mode")
		}
	case GatePreResume:
	default:
		return fmt.Errorf("invalid gate %q", c.Gate)
	}
	if c.Sensitivity != "" && c.Sensitivity != SensitivityInheritInner {
		return fmt.Errorf("invalid sensitivity %q", c.Sensitivity)
	}
	if c.Gate != "" && c.Sensitivity != "" {
		return errors.New("a launch gate and the sensitivity control are separate cases")
	}
	return nil
}

func generationArg(n int) string {
	if n == 0 {
		return "auto"
	}
	return strconv.Itoa(n)
}

// Args renders c as main-role arguments that Parse accepts.
func (c MainConfig) Args() []string {
	args := []string{RoleMain, "--launch-mode", c.LaunchMode, "--case-dir", c.CaseDir, "--generation", generationArg(c.Generation)}
	if c.Work != "" {
		args = append(args, "--engine-mode", c.Work)
	}
	if c.OnStop != "" {
		args = append(args, "--on-stop", c.OnStop)
	}
	if c.Gate != "" {
		args = append(args, "--hold-gate", c.Gate)
	}
	if c.Sensitivity != "" {
		args = append(args, "--sensitivity", c.Sensitivity)
	}
	return args
}

// Args renders c as engine-role arguments.
func (c EngineConfig) Args() []string {
	return []string{RoleEngine, "--case-dir", c.CaseDir, "--generation", strconv.Itoa(c.Generation),
		"--job-handle-number", strconv.FormatUint(c.JobHandle, 10), "--work", c.Work}
}

// Args renders c as leaf-role arguments.
func (c LeafConfig) Args() []string {
	return []string{"leaf", "--case-dir", c.CaseDir, "--generation", strconv.Itoa(c.Generation), "--role", c.Role,
		"--job-handle-number", strconv.FormatUint(c.JobHandle, 10), "--work", c.Work}
}

// Args renders c as ExecStop helper arguments.
func (c StopConfig) Args() []string {
	return []string{RoleStop, "--case-dir", c.CaseDir, "--behavior", c.Behavior}
}
