package nestedjob

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Native identities. Headless is a genuine local standard-user S4U manager in
// session zero; a SYSTEM token never stands in for it.
const (
	IdentitySystem   = "system"
	IdentityHeadless = "headless"
)

// Evidence lanes. Owner runs native Go tests that hold the actual unit job;
// daemon runs the standalone fixture under the installed daemon.
const (
	LaneOwner  = "owner"
	LaneDaemon = "daemon"
)

// Lane results. Only pass counts towards a passed execution.
const (
	ResultPass         = "pass"
	ResultFail         = "fail"
	ResultSkip         = "skip"
	ResultInconclusive = "inconclusive"
)

//go:embed matrix.json
var matrixJSON []byte

// Row is one case definition.
type Row struct {
	ID           string              `json:"id"`
	Action       string              `json:"action"`
	Expect       string              `json:"expect"`
	Modes        []string            `json:"modes"`
	Gates        map[string][]string `json:"gates,omitempty"`
	Identities   []string            `json:"identities"`
	Repeat       int                 `json:"repeat"`
	Lanes        []string            `json:"lanes"`
	OwnerPackage string              `json:"ownerPackage,omitempty"`
	OwnerTest    string              `json:"ownerTest,omitempty"`
	OwnerEnv     []string            `json:"ownerEnv,omitempty"`
}

// Matrix is the versioned case table.
type Matrix struct {
	Version int   `json:"version"`
	Issue   int   `json:"issue"`
	Rows    []Row `json:"rows"`
}

// Execution is one expanded case execution and the lanes it requires.
type Execution struct {
	Key          string   `json:"key"`
	Row          string   `json:"row"`
	Mode         string   `json:"mode"`
	Gate         string   `json:"gate,omitempty"`
	Identity     string   `json:"identity"`
	Repetition   int      `json:"repetition"`
	Lanes        []string `json:"lanes"`
	OwnerPackage string   `json:"ownerPackage,omitempty"`
	OwnerRun     string   `json:"ownerRun,omitempty"`
	OwnerEnv     []string `json:"ownerEnv,omitempty"`
}

// CaseMatrix returns the embedded, validated matrix.
func CaseMatrix() (*Matrix, error) {
	return DecodeMatrix(matrixJSON)
}

var (
	rowID     = regexp.MustCompile(`^N[0-9]{2}$`)
	testName  = regexp.MustCompile(`^Test[A-Za-z0-9]+$`)
	ownerEnvs = regexp.MustCompile(`^WINUNITD_NATIVE_NESTED_[A-Z_]+=[A-Za-z0-9]+$`)
)

// DecodeMatrix strictly decodes and validates a case table.
func DecodeMatrix(data []byte) (*Matrix, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var m Matrix
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("matrix has trailing data")
	}
	if m.Version != 1 {
		return nil, fmt.Errorf("matrix version %d", m.Version)
	}
	if len(m.Rows) == 0 {
		return nil, errors.New("matrix has no rows")
	}
	seen := map[string]bool{}
	for _, r := range m.Rows {
		if err := r.validate(); err != nil {
			return nil, fmt.Errorf("row %s: %w", r.ID, err)
		}
		if seen[r.ID] {
			return nil, fmt.Errorf("duplicate row %s", r.ID)
		}
		seen[r.ID] = true
	}
	return &m, nil
}

func uniqueSubset(values, allowed []string) bool {
	if len(values) == 0 {
		return false
	}
	for i, v := range values {
		if !slices.Contains(allowed, v) || slices.Contains(values[:i], v) {
			return false
		}
	}
	return true
}

func (r Row) validate() error {
	if !rowID.MatchString(r.ID) || r.Action == "" || r.Expect == "" {
		return errors.New("id, action and expected result are required")
	}
	if !uniqueSubset(r.Modes, LaunchModes) {
		return fmt.Errorf("modes %v", r.Modes)
	}
	if !uniqueSubset(r.Identities, []string{IdentitySystem, IdentityHeadless}) {
		return fmt.Errorf("identities %v", r.Identities)
	}
	if !uniqueSubset(r.Lanes, []string{LaneOwner, LaneDaemon}) {
		return fmt.Errorf("lanes %v", r.Lanes)
	}
	if r.Repeat < 1 || r.Repeat > 10 {
		return fmt.Errorf("repeat %d", r.Repeat)
	}
	owner := slices.Contains(r.Lanes, LaneOwner)
	if owner != (r.OwnerTest != "") || owner != (r.OwnerPackage != "") {
		return errors.New("owner lane, owner test and owner package must appear together")
	}
	if owner {
		if !testName.MatchString(r.OwnerTest) {
			return fmt.Errorf("owner test %q", r.OwnerTest)
		}
		if r.OwnerPackage != "internal/runtime" && r.OwnerPackage != "internal/manager" {
			return fmt.Errorf("owner package %q", r.OwnerPackage)
		}
	}
	for _, e := range r.OwnerEnv {
		if !owner || !ownerEnvs.MatchString(e) {
			return fmt.Errorf("owner environment %q", e)
		}
	}
	for mode, gates := range r.Gates {
		if !slices.Contains(r.Modes, mode) {
			return fmt.Errorf("gates for unused mode %q", mode)
		}
		allowed := []string{GateBeforeResume}
		if mode == ModeAssign {
			allowed = append(allowed, GateBeforeAssign)
		}
		if !uniqueSubset(gates, allowed) {
			return fmt.Errorf("gates %v for %s", gates, mode)
		}
	}
	if r.Gates != nil && len(r.Gates) != len(r.Modes) {
		return errors.New("a gated row needs gates for every mode")
	}
	return nil
}

// Expand lists every execution in row, mode, gate, identity, repetition order.
func (m *Matrix) Expand() []Execution {
	var out []Execution
	for _, r := range m.Rows {
		for _, mode := range r.Modes {
			gates := []string{""}
			if r.Gates != nil {
				gates = r.Gates[mode]
			}
			for _, gate := range gates {
				for _, id := range r.Identities {
					for rep := 1; rep <= r.Repeat; rep++ {
						parts := []string{r.ID, mode}
						if gate != "" {
							parts = append(parts, gate)
						}
						parts = append(parts, id, fmt.Sprint(rep))
						e := Execution{
							Key: strings.Join(parts, "/"), Row: r.ID, Mode: mode, Gate: gate,
							Identity: id, Repetition: rep, Lanes: slices.Clone(r.Lanes),
						}
						if r.OwnerTest != "" {
							e.OwnerPackage = r.OwnerPackage
							e.OwnerRun = "^" + r.OwnerTest + "$/^" + mode + "$"
							if gate != "" {
								e.OwnerRun += "/^" + gate + "$"
							}
							e.OwnerEnv = slices.Clone(r.OwnerEnv)
						}
						out = append(out, e)
					}
				}
			}
		}
	}
	return out
}

// LaneResult is one lane's recorded outcome for one execution.
type LaneResult struct {
	Key    string `json:"key"`
	Lane   string `json:"lane"`
	Result string `json:"result"`
	Detail string `json:"detail,omitempty"`
}

// Summary counts executions; an execution passes only when every lane it
// requires recorded pass. Skipped, inconclusive and missing lanes are never
// counted as passed.
type Summary struct {
	Executions int      `json:"executions"`
	Passed     int      `json:"passed"`
	Failed     int      `json:"failed"`
	Incomplete int      `json:"incomplete"`
	Missing    []string `json:"missing,omitempty"`
	OK         bool     `json:"ok"`
}

// Summarize checks results against the expanded executions. Unknown, duplicate
// or malformed results are errors rather than silently ignored entries.
func Summarize(execs []Execution, results []LaneResult) (Summary, error) {
	want := map[string]Execution{}
	for _, e := range execs {
		if _, dup := want[e.Key]; dup {
			return Summary{}, fmt.Errorf("duplicate execution %s", e.Key)
		}
		want[e.Key] = e
	}
	got := map[string]string{}
	for _, r := range results {
		e, ok := want[r.Key]
		if !ok || !slices.Contains(e.Lanes, r.Lane) {
			return Summary{}, fmt.Errorf("result for unexpected execution %s lane %s", r.Key, r.Lane)
		}
		switch r.Result {
		case ResultPass, ResultFail, ResultSkip, ResultInconclusive:
		default:
			return Summary{}, fmt.Errorf("result %q for %s", r.Result, r.Key)
		}
		k := r.Key + "#" + r.Lane
		if _, dup := got[k]; dup {
			return Summary{}, fmt.Errorf("duplicate result for %s lane %s", r.Key, r.Lane)
		}
		got[k] = r.Result
	}
	s := Summary{Executions: len(execs)}
	for _, e := range execs {
		passed, failed := true, false
		for _, lane := range e.Lanes {
			switch result, ok := got[e.Key+"#"+lane]; {
			case !ok:
				passed = false
				s.Missing = append(s.Missing, e.Key+"#"+lane)
			case result == ResultFail:
				passed, failed = false, true
			case result != ResultPass:
				passed = false
			}
		}
		switch {
		case failed:
			s.Failed++
		case passed:
			s.Passed++
		default:
			s.Incomplete++
		}
	}
	s.OK = s.Passed == s.Executions
	return s, nil
}
