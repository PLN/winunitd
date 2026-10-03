package nestedjob

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Native identities. Headless is a genuine local standard-user S4U process in
// session zero; a SYSTEM token never stands in for it.
const (
	IdentitySystem   = "system"
	IdentityHeadless = "headless"
)

// Evidence lanes. Native-owner runs native Go tests whose process owns the
// actual unit job; immutable-daemon runs the standalone fixture under the
// installed daemon and its SCM configuration.
const (
	LaneOwner  = "native-owner"
	LaneDaemon = "immutable-daemon"
)

//go:embed matrix.json
var matrixJSON []byte

// MatrixHash identifies the embedded case matrix. Results bind to it.
func MatrixHash() string {
	sum := sha256.Sum256(matrixJSON)
	return hex.EncodeToString(sum[:])
}

// CaseDef describes one case.
type CaseDef struct {
	Action       string   `json:"action"`
	Expect       string   `json:"expect"`
	OwnerPackage string   `json:"ownerPackage,omitempty"`
	OwnerTest    string   `json:"ownerTest,omitempty"`
	OwnerEnv     []string `json:"ownerEnv,omitempty"`
	DaemonDriver string   `json:"daemonDriver,omitempty"`
	// RequiredControls are separately keyed control executions that every
	// passing execution of this case needs, such as an uncapped measurement.
	RequiredControls []string `json:"requiredControls,omitempty"`
}

// Group expands its cases over modes, phases, identities and repetitions in
// one lane.
type Group struct {
	Cases       []string            `json:"cases"`
	Modes       []string            `json:"modes"`
	Phases      map[string][]string `json:"phases,omitempty"`
	Identities  []string            `json:"identities"`
	Lane        string              `json:"lane"`
	Repetitions []string            `json:"repetitions"`
}

// Matrix is the versioned primary case table. It is the single source of the
// required execution set; tests and drivers never hardcode another count.
type Matrix struct {
	Version        int                `json:"version"`
	Issue          int                `json:"issue"`
	OwnerProofCase string             `json:"ownerProofCase"`
	FirstIncrement []string           `json:"firstIncrement"`
	Cases          map[string]CaseDef `json:"cases"`
	Groups         []Group            `json:"groups"`
}

// Execution is one primary scenario execution in exactly one lane.
type Execution struct {
	Key          string   `json:"key"`
	Case         string   `json:"case"`
	Mode         string   `json:"mode"`
	Identity     string   `json:"identity"`
	Phase        string   `json:"phase,omitempty"`
	Repetition   string   `json:"repetition"`
	Lane         string   `json:"lane"`
	OwnerPackage string   `json:"ownerPackage,omitempty"`
	OwnerRun     string   `json:"ownerRun,omitempty"`
	OwnerEnv     []string `json:"ownerEnv,omitempty"`
	DaemonDriver string   `json:"daemonDriver,omitempty"`
	// RequiredControls are the control names a passing record must link.
	RequiredControls []string `json:"requiredControls,omitempty"`
	// OwnerProof links an immutable-daemon execution to the native-owner
	// execution that proves membership in the particular unit job.
	OwnerProof string `json:"ownerProof,omitempty"`
}

// ExecutionKey composes the unique key of one execution.
func ExecutionKey(caseID, mode, identity, phase, repetition, lane string) string {
	parts := []string{caseID, mode, identity}
	if phase != "" {
		parts = append(parts, phase)
	}
	return strings.Join(append(parts, repetition, lane), "/")
}

// OwnerRun is the go test -run selector of a native-owner execution:
// test, then mode, identity and, when present, phase subtests.
func OwnerRun(test, mode, identity, phase string) string {
	run := "^" + test + "$/^" + mode + "$/^" + identity + "$"
	if phase != "" {
		run += "/^" + phase + "$"
	}
	return run
}

// CaseMatrix returns the embedded, validated matrix.
func CaseMatrix() (*Matrix, error) {
	return DecodeMatrix(matrixJSON)
}

var (
	caseIDPattern     = regexp.MustCompile(`^N[0-9]{2}$`)
	testName          = regexp.MustCompile(`^Test[A-Za-z0-9]+$`)
	ownerEnvs         = regexp.MustCompile(`^WINUNITD_NATIVE_NESTED_[A-Z_]+=[A-Za-z0-9]+$`)
	repetitionPattern = regexp.MustCompile(`^r[1-9]$`)
	controlName       = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
)

// DaemonDriverPath is the generic installed-daemon driver in the product tree.
const DaemonDriverPath = "tools/lab/assets/nested-job-checks.ps1"

// DecodeMatrix strictly decodes and validates a case table, including that
// its expansion has unique keys and resolvable owner proofs.
func DecodeMatrix(data []byte) (*Matrix, error) {
	var m Matrix
	if err := decodeStrict(data, &m); err != nil {
		return nil, fmt.Errorf("matrix: %w", err)
	}
	if m.Version != 2 {
		return nil, fmt.Errorf("matrix version %d", m.Version)
	}
	if len(m.Cases) == 0 || len(m.Groups) == 0 {
		return nil, errors.New("matrix has no cases or groups")
	}
	for id, c := range m.Cases {
		if err := c.validate(id); err != nil {
			return nil, fmt.Errorf("case %s: %w", id, err)
		}
	}
	for i, g := range m.Groups {
		if err := g.validate(&m); err != nil {
			return nil, fmt.Errorf("group %d: %w", i+1, err)
		}
	}
	if _, ok := m.Cases[m.OwnerProofCase]; !ok {
		return nil, fmt.Errorf("owner proof case %q is not defined", m.OwnerProofCase)
	}
	for i, id := range m.FirstIncrement {
		if _, ok := m.Cases[id]; !ok || slices.Contains(m.FirstIncrement[:i], id) {
			return nil, fmt.Errorf("first increment case %q is undefined or repeated", id)
		}
	}
	execs, err := m.expand()
	if err != nil {
		return nil, err
	}
	used := map[string]bool{}
	for _, e := range execs {
		used[e.Case] = true
	}
	for id := range m.Cases {
		if !used[id] {
			return nil, fmt.Errorf("case %s has no execution", id)
		}
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

func (c CaseDef) validate(id string) error {
	if !caseIDPattern.MatchString(id) || c.Action == "" || c.Expect == "" {
		return errors.New("id, action and expected result are required")
	}
	if (c.OwnerTest != "") != (c.OwnerPackage != "") {
		return errors.New("owner test and owner package must appear together")
	}
	if c.OwnerTest != "" && !testName.MatchString(c.OwnerTest) {
		return fmt.Errorf("owner test %q", c.OwnerTest)
	}
	if c.OwnerPackage != "" && c.OwnerPackage != "internal/runtime" && c.OwnerPackage != "internal/manager" {
		return fmt.Errorf("owner package %q", c.OwnerPackage)
	}
	for _, e := range c.OwnerEnv {
		if c.OwnerTest == "" || !ownerEnvs.MatchString(e) {
			return fmt.Errorf("owner environment %q", e)
		}
	}
	if c.DaemonDriver != "" && c.DaemonDriver != DaemonDriverPath {
		return fmt.Errorf("daemon driver %q", c.DaemonDriver)
	}
	for i, name := range c.RequiredControls {
		if c.OwnerTest == "" || !controlName.MatchString(name) || slices.Contains(c.RequiredControls[:i], name) {
			return fmt.Errorf("required control %q", name)
		}
	}
	return nil
}

func (g Group) validate(m *Matrix) error {
	if len(g.Cases) == 0 {
		return errors.New("no cases")
	}
	for i, id := range g.Cases {
		c, ok := m.Cases[id]
		if !ok || slices.Contains(g.Cases[:i], id) {
			return fmt.Errorf("case %q is undefined or repeated", id)
		}
		switch g.Lane {
		case LaneOwner:
			if c.OwnerTest == "" {
				return fmt.Errorf("case %s has no owner test", id)
			}
		case LaneDaemon:
			if c.DaemonDriver == "" {
				return fmt.Errorf("case %s has no daemon driver", id)
			}
		default:
			return fmt.Errorf("lane %q", g.Lane)
		}
	}
	if !uniqueSubset(g.Modes, LaunchModes) {
		return fmt.Errorf("modes %v", g.Modes)
	}
	if !uniqueSubset(g.Identities, []string{IdentitySystem, IdentityHeadless}) {
		return fmt.Errorf("identities %v", g.Identities)
	}
	if len(g.Repetitions) == 0 {
		return errors.New("no repetitions")
	}
	for i, r := range g.Repetitions {
		if !repetitionPattern.MatchString(r) || slices.Contains(g.Repetitions[:i], r) {
			return fmt.Errorf("repetition %q", r)
		}
	}
	if g.Phases != nil && len(g.Phases) != len(g.Modes) {
		return errors.New("a phased group needs phases for every mode")
	}
	for mode, phases := range g.Phases {
		if !slices.Contains(g.Modes, mode) {
			return fmt.Errorf("phases for unused mode %q", mode)
		}
		allowed := []string{GatePreResume}
		if mode == ModeAssign {
			allowed = append(allowed, GatePreAssign)
		}
		if !uniqueSubset(phases, allowed) {
			return fmt.Errorf("phases %v for %s", phases, mode)
		}
	}
	return nil
}

// Expand lists every primary execution in group, case, mode, phase,
// identity and repetition order.
func (m *Matrix) Expand() []Execution {
	execs, _ := m.expand()
	return execs
}

func (m *Matrix) expand() ([]Execution, error) {
	var out []Execution
	seen := map[string]bool{}
	for _, g := range m.Groups {
		for _, id := range g.Cases {
			c := m.Cases[id]
			for _, mode := range g.Modes {
				phases := []string{""}
				if g.Phases != nil {
					phases = g.Phases[mode]
				}
				for _, phase := range phases {
					for _, identity := range g.Identities {
						for _, rep := range g.Repetitions {
							e := Execution{
								Key:  ExecutionKey(id, mode, identity, phase, rep, g.Lane),
								Case: id, Mode: mode, Identity: identity, Phase: phase, Repetition: rep, Lane: g.Lane,
							}
							if seen[e.Key] {
								return nil, fmt.Errorf("duplicate execution %s", e.Key)
							}
							seen[e.Key] = true
							if g.Lane == LaneOwner {
								e.OwnerPackage = c.OwnerPackage
								e.OwnerRun = OwnerRun(c.OwnerTest, mode, identity, phase)
								e.OwnerEnv = slices.Clone(c.OwnerEnv)
								e.RequiredControls = slices.Clone(c.RequiredControls)
							} else {
								e.DaemonDriver = c.DaemonDriver
								e.OwnerProof = ExecutionKey(m.OwnerProofCase, mode, identity, "", "r1", LaneOwner)
							}
							out = append(out, e)
						}
					}
				}
			}
		}
	}
	for _, e := range out {
		if e.OwnerProof != "" && !seen[e.OwnerProof] {
			return nil, fmt.Errorf("%s links missing owner proof %s", e.Key, e.OwnerProof)
		}
	}
	return out, nil
}

// Selection narrows the required set for development runs. Any non-empty
// field makes a summary partial: it cannot close #265 acceptance.
type Selection struct {
	Lanes          []string `json:"lanes,omitempty"`
	Identities     []string `json:"identities,omitempty"`
	Cases          []string `json:"cases,omitempty"`
	FirstIncrement bool     `json:"firstIncrement,omitempty"`
}

// Empty reports whether s selects the complete required set.
func (s Selection) Empty() bool {
	return len(s.Lanes) == 0 && len(s.Identities) == 0 && len(s.Cases) == 0 && !s.FirstIncrement
}

// Select returns the executions s selects, in matrix order.
func (m *Matrix) Select(s Selection) []Execution {
	var out []Execution
	for _, e := range m.Expand() {
		if len(s.Lanes) > 0 && !slices.Contains(s.Lanes, e.Lane) ||
			len(s.Identities) > 0 && !slices.Contains(s.Identities, e.Identity) ||
			len(s.Cases) > 0 && !slices.Contains(s.Cases, e.Case) ||
			s.FirstIncrement && !slices.Contains(m.FirstIncrement, e.Case) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Counts reports executions per lane and per identity.
func Counts(execs []Execution) map[string]int {
	out := map[string]int{"total": len(execs)}
	for _, e := range execs {
		out[e.Lane]++
		out[e.Identity]++
	}
	return out
}
