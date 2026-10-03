package nestedjob

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// Result outcomes. Only pass, with confirmed cleanup, counts as passed.
const (
	ResultPass         = "pass"
	ResultFail         = "fail"
	ResultSkip         = "skip"
	ResultInconclusive = "inconclusive"
)

// Result kinds. Controls (such as an uncapped CPU control) and supplementary
// regressions have their own keys and are counted outside the primary set.
const (
	KindPrimary       = "primary"
	KindControl       = "control"
	KindSupplementary = "supplementary"
)

// Token sources: the observed token of an owner process or a genuine S4U
// logon obtained by the SYSTEM runner.
const (
	TokenProcess = "process"
	TokenS4U     = "s4u"
)

// SystemSID is the LocalSystem account.
const SystemSID = "S-1-5-18"

// ResultSchema is the result record schema.
const ResultSchema = 1

// TokenContext is the actual token that ran a scenario's owner and fixture.
type TokenContext struct {
	SID      string `json:"sid"`
	Session  uint32 `json:"session"`
	Elevated bool   `json:"elevated"`
	Source   string `json:"source"`
}

// Result is one recorded execution. A control's key is its primary key, '#'
// and the control name; a supplementary key names its own scenario.
type Result struct {
	Schema           int           `json:"schema"`
	Key              string        `json:"key"`
	Kind             string        `json:"kind"`
	Case             string        `json:"case"`
	Mode             string        `json:"mode"`
	Identity         string        `json:"identity"`
	Phase            string        `json:"phase,omitempty"`
	Repetition       string        `json:"repetition"`
	Lane             string        `json:"lane"`
	Result           string        `json:"result"`
	Source           string        `json:"source"`
	Matrix           string        `json:"matrix"`
	Token            *TokenContext `json:"token,omitempty"`
	CleanupConfirmed bool          `json:"cleanupConfirmed"`
	Controls         []string      `json:"controls,omitempty"`
	OwnerProof       string        `json:"ownerProof,omitempty"`
	Detail           string        `json:"detail,omitempty"`
}

// PrimaryKey is the key of the primary execution a record describes.
func (r Result) PrimaryKey() string {
	return ExecutionKey(r.Case, r.Mode, r.Identity, r.Phase, r.Repetition, r.Lane)
}

// IdentityOf names the identity a token context genuinely supports, or ""
// when it supports neither (for example an administrator test run).
func IdentityOf(t *TokenContext) string {
	switch {
	case t == nil:
		return ""
	case t.SID == SystemSID && t.Source == TokenProcess:
		return IdentitySystem
	case t.Source == TokenS4U && t.SID != "" && t.SID != SystemSID && t.Session == 0 && !t.Elevated:
		return IdentityHeadless
	}
	return ""
}

// Summary is the fail-closed evaluation of results against the matrix.
type Summary struct {
	Matrix        string   `json:"matrix"`
	Source        string   `json:"source"`
	Required      int      `json:"required"`
	Selected      int      `json:"selected"`
	Passed        int      `json:"passed"`
	Failed        int      `json:"failed"`
	Incomplete    int      `json:"incomplete"`
	Missing       []string `json:"missing,omitempty"`
	Problems      []string `json:"problems,omitempty"`
	Controls      int      `json:"controls"`
	Supplementary int      `json:"supplementary"`
	Partial       bool     `json:"partial"`
	Omitted       []string `json:"omitted,omitempty"`
	// Complete is true only for the full required set with every execution
	// passed and no problem. Only a complete summary can support #265.
	Complete bool `json:"complete"`
}

// Summarize evaluates results for source against m under selection. Unknown,
// duplicate, stale or inconsistent records are problems, never ignored.
func Summarize(m *Matrix, results []Result, source string, sel Selection) Summary {
	all := m.Expand()
	selected := m.Select(sel)
	s := Summary{Matrix: MatrixHash(), Source: source, Required: len(all), Selected: len(selected), Partial: !sel.Empty()}
	known := map[string]Execution{}
	for _, e := range all {
		known[e.Key] = e
	}
	inSelection := map[string]bool{}
	for _, e := range selected {
		inSelection[e.Key] = true
	}
	if s.Partial {
		for _, e := range all {
			if !inSelection[e.Key] {
				s.Omitted = append(s.Omitted, e.Key)
			}
		}
	}
	problem := func(format string, args ...any) { s.Problems = append(s.Problems, fmt.Sprintf(format, args...)) }
	primaries := map[string]Result{}
	controls := map[string]Result{}
	bad := map[string]bool{}
	seen := map[string]bool{}
	for _, r := range results {
		if seen[r.Key] {
			problem("%s: duplicate record", r.Key)
			bad[primaryOf(r)] = true
			continue
		}
		seen[r.Key] = true
		if err := r.check(source, known); err != nil {
			problem("%s: %v", r.Key, err)
			bad[primaryOf(r)] = true
			continue
		}
		switch r.Kind {
		case KindPrimary:
			primaries[r.Key] = r
		case KindControl:
			controls[r.Key] = r
			s.Controls++
		case KindSupplementary:
			s.Supplementary++
		}
	}
	for _, e := range selected {
		r, ok := primaries[e.Key]
		switch {
		case bad[e.Key]:
			s.Incomplete++
		case !ok:
			s.Missing = append(s.Missing, e.Key)
			s.Incomplete++
		case r.Result == ResultFail:
			s.Failed++
		case r.Result != ResultPass:
			s.Incomplete++
		case !r.CleanupConfirmed:
			problem("%s: passed without confirmed cleanup", r.Key)
			s.Incomplete++
		case !controlsPassed(r, controls, problem):
			s.Incomplete++
		case e.OwnerProof != "" && !ownerProofPassed(r, e, primaries, problem):
			s.Incomplete++
		default:
			s.Passed++
		}
	}
	s.Complete = !s.Partial && s.Passed == s.Required && len(s.Problems) == 0
	return s
}

func primaryOf(r Result) string {
	key, _, _ := strings.Cut(r.Key, "#")
	return key
}

// check validates one record's shape and its binding to source and matrix.
func (r Result) check(source string, known map[string]Execution) error {
	if r.Schema != ResultSchema {
		return fmt.Errorf("schema %d", r.Schema)
	}
	switch r.Result {
	case ResultPass, ResultFail, ResultSkip, ResultInconclusive:
	default:
		return fmt.Errorf("result %q", r.Result)
	}
	if r.Source == "" || r.Source != source {
		return fmt.Errorf("source %q is not the selected source", r.Source)
	}
	if r.Matrix != MatrixHash() {
		return errors.New("recorded against another matrix")
	}
	primary := r.PrimaryKey()
	switch r.Kind {
	case KindPrimary:
		if r.Key != primary {
			return fmt.Errorf("key does not match its fields (%s)", primary)
		}
	case KindControl:
		name, ok := strings.CutPrefix(r.Key, primary+"#")
		if !ok || name == "" || strings.ContainsAny(name, "#/") {
			return fmt.Errorf("control key does not name its primary execution %s", primary)
		}
	case KindSupplementary:
		if r.Key == "" || known[r.Key].Key != "" {
			return errors.New("supplementary record reuses a primary key")
		}
		return nil
	default:
		return fmt.Errorf("kind %q", r.Kind)
	}
	e, ok := known[primary]
	if !ok {
		return errors.New("not a required execution of this matrix")
	}
	if r.Result == ResultPass || r.Token != nil {
		if got := IdentityOf(r.Token); got != e.Identity {
			return fmt.Errorf("token context supports %q, not %s", got, e.Identity)
		}
	}
	if r.Kind == KindPrimary && r.OwnerProof != e.OwnerProof {
		return fmt.Errorf("owner proof %q, want %q", r.OwnerProof, e.OwnerProof)
	}
	return nil
}

func controlsPassed(r Result, controls map[string]Result, problem func(string, ...any)) bool {
	ok := true
	for _, key := range r.Controls {
		c, found := controls[key]
		switch {
		case !found || primaryOf(c) != r.Key:
			problem("%s: control %s is missing or belongs to another execution", r.Key, key)
			ok = false
		case c.Result != ResultPass:
			ok = false
		}
	}
	return ok
}

func ownerProofPassed(r Result, e Execution, primaries map[string]Result, problem func(string, ...any)) bool {
	proof, ok := primaries[e.OwnerProof]
	if !ok || proof.Result != ResultPass || !proof.CleanupConfirmed {
		problem("%s: owner proof %s has not passed", r.Key, e.OwnerProof)
		return false
	}
	return true
}

// ResultFileName is a filesystem-safe name for a result key.
func ResultFileName(key string) string {
	r := strings.NewReplacer("/", "_", "#", "+")
	return r.Replace(key) + ".json"
}

// WriteResult records r in dir, refusing to replace an existing record.
func WriteResult(dir string, r Result) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, ResultFileName(r.Key)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(data, '\n'))
	return errors.Join(werr, f.Close())
}

// ReadResults strictly decodes every *.json record in dir, in name order.
func ReadResults(dir string) ([]Result, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) > 4096 {
		return nil, fmt.Errorf("more than 4096 result records")
	}
	var out []Result
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		if len(data) > MaxStatusBytes {
			return nil, fmt.Errorf("%s exceeds %d bytes", name, MaxStatusBytes)
		}
		dec := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})))
		dec.DisallowUnknownFields()
		var r Result
		if err := dec.Decode(&r); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if ResultFileName(r.Key) != name {
			return nil, fmt.Errorf("%s holds record %s", name, r.Key)
		}
		out = append(out, r)
	}
	return out, nil
}

// ParseKey splits a primary key into its fields.
func ParseKey(key string) (Execution, error) {
	parts := strings.Split(key, "/")
	e := Execution{Key: key}
	switch len(parts) {
	case 5:
		e.Case, e.Mode, e.Identity, e.Repetition, e.Lane = parts[0], parts[1], parts[2], parts[3], parts[4]
	case 6:
		e.Case, e.Mode, e.Identity, e.Phase, e.Repetition, e.Lane = parts[0], parts[1], parts[2], parts[3], parts[4], parts[5]
	default:
		return Execution{}, fmt.Errorf("key %q", key)
	}
	if !caseIDPattern.MatchString(e.Case) || !slices.Contains(LaunchModes, e.Mode) ||
		(e.Identity != IdentitySystem && e.Identity != IdentityHeadless) ||
		(len(parts) == 6 && e.Phase != GatePreAssign && e.Phase != GatePreResume) ||
		!repetitionPattern.MatchString(e.Repetition) || (e.Lane != LaneOwner && e.Lane != LaneDaemon) {
		return Execution{}, fmt.Errorf("key %q", key)
	}
	return e, nil
}
