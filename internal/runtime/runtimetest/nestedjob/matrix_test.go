package nestedjob

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCaseMatrixExpansion(t *testing.T) {
	m, err := CaseMatrix()
	if err != nil {
		t.Fatal(err)
	}
	execs := m.Expand()
	counts := Counts(execs)
	// The reviewed plan: 58 native-owner and 10 immutable-daemon executions.
	if counts["total"] != 68 || counts[LaneOwner] != 58 || counts[LaneDaemon] != 10 {
		t.Fatalf("counts = %v", counts)
	}
	first := m.Select(Selection{FirstIncrement: true})
	if c := Counts(first); c["total"] != 20 || c[IdentitySystem] != 11 || c[IdentityHeadless] != 9 || c[LaneDaemon] != 0 {
		t.Fatalf("first increment counts = %v", c)
	}
	keys := map[string]Execution{}
	for _, e := range execs {
		if _, dup := keys[e.Key]; dup {
			t.Fatalf("duplicate key %s", e.Key)
		}
		keys[e.Key] = e
		parsed, err := ParseKey(e.Key)
		if err != nil || parsed.Case != e.Case || parsed.Mode != e.Mode || parsed.Identity != e.Identity ||
			parsed.Phase != e.Phase || parsed.Repetition != e.Repetition || parsed.Lane != e.Lane {
			t.Fatalf("key %s does not round trip: %+v %v", e.Key, parsed, err)
		}
		switch e.Lane {
		case LaneOwner:
			if e.OwnerRun == "" || e.OwnerProof != "" || e.DaemonDriver != "" {
				t.Fatalf("owner execution %+v", e)
			}
		case LaneDaemon:
			if e.OwnerRun != "" || e.DaemonDriver != DaemonDriverPath || e.OwnerProof != ExecutionKey("N01", e.Mode, e.Identity, "", "r1", LaneOwner) {
				t.Fatalf("daemon execution %+v", e)
			}
		}
	}
	gate := keys["N14/assign/headless/pre-assign/r1/native-owner"]
	if gate.OwnerRun != "^TestNativeNestedJobLaunchGate$/^assign$/^headless$/^pre-assign$" || gate.OwnerPackage != "internal/runtime" {
		t.Fatalf("gate execution = %+v", gate)
	}
	for _, want := range []string{
		"N03/job-list/headless/r3/native-owner", "N04/job-list/headless/r2/native-owner",
		"N06/job-list/headless/r3/immutable-daemon", "N07/assign/system/r1/immutable-daemon",
		"N16/job-list/system/r1/native-owner", "N09/assign/headless/r1/native-owner",
	} {
		if _, ok := keys[want]; !ok {
			t.Errorf("missing %s", want)
		}
	}
	for _, absent := range []string{
		"N03/assign/system/r2/native-owner", "N05/job-list/headless/r2/native-owner", "N06/assign/system/r1/immutable-daemon",
		"N06/assign/headless/r2/immutable-daemon", "N15/assign/headless/r1/native-owner", "N07/assign/system/r1/native-owner",
	} {
		if _, ok := keys[absent]; ok {
			t.Errorf("unexpected %s", absent)
		}
	}
	if e := keys["N09/job-list/system/r1/native-owner"]; len(e.OwnerEnv) != 1 {
		t.Fatalf("CPU rows must request metering: %+v", e)
	}
	if len(MatrixHash()) != 64 {
		t.Fatal("matrix hash")
	}
}

func TestDecodeMatrixRejectsInvalidTables(t *testing.T) {
	base := func() map[string]any {
		var m map[string]any
		if err := json.Unmarshal(matrixJSON, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	encode := func(m map[string]any) []byte {
		data, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	if _, err := DecodeMatrix(encode(base())); err != nil {
		t.Fatal(err)
	}
	group := func(m map[string]any, i int) map[string]any { return m["groups"].([]any)[i].(map[string]any) }
	cases := func(m map[string]any) map[string]any { return m["cases"].(map[string]any) }
	mutations := map[string]func(map[string]any){
		"version":           func(m map[string]any) { m["version"] = 1 },
		"unknown field":     func(m map[string]any) { m["shell"] = "cmd" },
		"duplicate key":     func(m map[string]any) { m["groups"] = append(m["groups"].([]any), group(m, 0)) },
		"unknown lane":      func(m map[string]any) { group(m, 0)["lane"] = "ci" },
		"mode":              func(m map[string]any) { group(m, 0)["modes"] = []any{"fallback"} },
		"repetition":        func(m map[string]any) { group(m, 5)["repetitions"] = []any{"r2", "r2"} },
		"undefined case":    func(m map[string]any) { group(m, 0)["cases"] = []any{"N99"} },
		"daemon case owner": func(m map[string]any) { group(m, 1)["cases"] = []any{"N02"} },
		"job-list pre-assign": func(m map[string]any) {
			group(m, 3)["phases"] = map[string]any{"assign": []any{"pre-assign"}, "job-list": []any{"pre-assign"}}
		},
		"phase for one mode": func(m map[string]any) { group(m, 3)["phases"] = map[string]any{"assign": []any{"pre-resume"}} },
		"owner test name":    func(m map[string]any) { cases(m)["N01"].(map[string]any)["ownerTest"] = "Test X;" },
		"owner package":      func(m map[string]any) { cases(m)["N01"].(map[string]any)["ownerPackage"] = "cmd/winunitd" },
		"owner env":          func(m map[string]any) { cases(m)["N09"].(map[string]any)["ownerEnv"] = []any{"PATH=x"} },
		"daemon driver":      func(m map[string]any) { cases(m)["N06"].(map[string]any)["daemonDriver"] = "run.ps1" },
		"unused case": func(m map[string]any) {
			cases(m)["N17"] = map[string]any{"action": "a", "expect": "e", "ownerPackage": "internal/runtime", "ownerTest": "TestX"}
		},
		"missing owner proof": func(m map[string]any) { m["ownerProofCase"] = "N15" },
		"first increment":     func(m map[string]any) { m["firstIncrement"] = []any{"N01", "N01"} },
	}
	for name, mutate := range mutations {
		m := base()
		mutate(m)
		if _, err := DecodeMatrix(encode(m)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

const testSource = "0123456789abcdef0123456789abcdef01234567"

func passing(t *testing.T, e Execution) Result {
	t.Helper()
	tok := &TokenContext{SID: SystemSID, Source: TokenProcess}
	if e.Identity == IdentityHeadless {
		tok = &TokenContext{SID: "S-1-5-21-1-2-3-1001", Source: TokenS4U}
	}
	return Result{
		Schema: ResultSchema, Key: e.Key, Kind: KindPrimary, Case: e.Case, Mode: e.Mode, Identity: e.Identity,
		Phase: e.Phase, Repetition: e.Repetition, Lane: e.Lane, Result: ResultPass, Source: testSource,
		Matrix: MatrixHash(), Token: tok, CleanupConfirmed: true, OwnerProof: e.OwnerProof,
	}
}

func allPassing(t *testing.T, m *Matrix) []Result {
	var out []Result
	for _, e := range m.Expand() {
		out = append(out, passing(t, e))
	}
	return out
}

func TestSummarizeRequiresTheExactPassingSet(t *testing.T) {
	m, err := CaseMatrix()
	if err != nil {
		t.Fatal(err)
	}
	results := allPassing(t, m)
	if s := Summarize(m, results, testSource, Selection{}); !s.Complete || s.Passed != 68 || len(s.Problems) != 0 {
		t.Fatalf("complete set: %+v", s)
	}
	find := func(rs []Result, key string) int {
		for i, r := range rs {
			if r.Key == key {
				return i
			}
		}
		t.Fatalf("no %s", key)
		return -1
	}
	clone := func() []Result {
		out := append([]Result(nil), results...)
		for i := range out {
			if out[i].Token != nil {
				tok := *out[i].Token
				out[i].Token = &tok
			}
		}
		return out
	}
	const h = "N01/job-list/headless/r1/native-owner"
	const d = "N07/job-list/headless/r2/immutable-daemon"
	cases := map[string]func([]Result) []Result{
		"missing":      func(rs []Result) []Result { i := find(rs, h); return append(rs[:i:i], rs[i+1:]...) },
		"duplicate":    func(rs []Result) []Result { return append(rs, rs[find(rs, h)]) },
		"skipped":      func(rs []Result) []Result { rs[find(rs, h)].Result = ResultSkip; return rs },
		"inconclusive": func(rs []Result) []Result { rs[find(rs, h)].Result = ResultInconclusive; return rs },
		"failed":       func(rs []Result) []Result { rs[find(rs, h)].Result = ResultFail; return rs },
		"system token for headless": func(rs []Result) []Result {
			rs[find(rs, h)].Token = &TokenContext{SID: SystemSID, Source: TokenProcess}
			return rs
		},
		"headless SID run as SYSTEM": func(rs []Result) []Result {
			rs[find(rs, h)].Token = &TokenContext{SID: "S-1-5-21-1-2-3-1001", Source: TokenProcess}
			return rs
		},
		"elevated headless": func(rs []Result) []Result { rs[find(rs, h)].Token.Elevated = true; return rs },
		"administrator run": func(rs []Result) []Result {
			rs[find(rs, "N01/assign/system/r1/native-owner")].Token = &TokenContext{SID: "S-1-5-21-9-9-9-500", Source: TokenProcess, Elevated: true}
			return rs
		},
		"wrong lane": func(rs []Result) []Result {
			i := find(rs, h)
			rs[i].Lane = LaneDaemon
			return rs
		},
		"wrong phase": func(rs []Result) []Result {
			i := find(rs, "N14/assign/system/pre-assign/r1/native-owner")
			rs[i].Phase = GatePreResume
			return rs
		},
		"stale source": func(rs []Result) []Result { rs[find(rs, h)].Source = "fedcba9876543210"; return rs },
		"stale matrix": func(rs []Result) []Result { rs[find(rs, h)].Matrix = strings.Repeat("0", 64); return rs },
		"unconfirmed cleanup": func(rs []Result) []Result {
			rs[find(rs, h)].CleanupConfirmed = false
			return rs
		},
		"missing control": func(rs []Result) []Result {
			rs[find(rs, "N09/assign/system/r1/native-owner")].Controls = []string{"N09/assign/system/r1/native-owner#uncapped"}
			return rs
		},
		"control of another execution": func(rs []Result) []Result {
			key := "N10/assign/system/r1/native-owner#uncapped"
			c := passing(t, Execution{Key: key, Case: "N10", Mode: ModeAssign, Identity: IdentitySystem, Repetition: "r1", Lane: LaneOwner})
			c.Kind = KindControl
			rs[find(rs, "N09/assign/system/r1/native-owner")].Controls = []string{key}
			return append(rs, c)
		},
		"failed owner proof": func(rs []Result) []Result {
			rs[find(rs, "N01/job-list/headless/r1/native-owner")].Result = ResultFail
			return rs
		},
		"wrong owner proof": func(rs []Result) []Result { rs[find(rs, d)].OwnerProof = ""; return rs },
		"unknown execution": func(rs []Result) []Result {
			r := rs[find(rs, h)]
			r.Repetition, r.Key = "r2", "N01/job-list/headless/r2/native-owner"
			return append(rs, r)
		},
		"unknown schema": func(rs []Result) []Result { rs[find(rs, h)].Schema = 2; return rs },
	}
	for name, mutate := range cases {
		s := Summarize(m, mutate(clone()), testSource, Selection{})
		if s.Complete {
			t.Errorf("%s: summary complete", name)
		}
	}
	// A matching control passes; an inconclusive one leaves the execution open.
	withControl := clone()
	key := "N09/assign/system/r1/native-owner"
	control := passing(t, Execution{Key: key + "#uncapped", Case: "N09", Mode: ModeAssign, Identity: IdentitySystem, Repetition: "r1", Lane: LaneOwner})
	control.Kind = KindControl
	withControl[find(withControl, key)].Controls = []string{control.Key}
	if s := Summarize(m, append(withControl, control), testSource, Selection{}); !s.Complete || s.Controls != 1 {
		t.Fatalf("passing control: %+v", s)
	}
	control.Result = ResultInconclusive
	if s := Summarize(m, append(withControl, control), testSource, Selection{}); s.Complete || s.Incomplete != 1 {
		t.Fatalf("inconclusive control: %+v", s)
	}
	supplementary := Result{Schema: ResultSchema, Key: "owner-crash/assign/system", Kind: KindSupplementary, Result: ResultPass, Source: testSource, Matrix: MatrixHash()}
	if s := Summarize(m, append(clone(), supplementary), testSource, Selection{}); !s.Complete || s.Supplementary != 1 {
		t.Fatalf("supplementary record: %+v", s)
	}
}

func TestSummarizeSelectionIsPartial(t *testing.T) {
	m, err := CaseMatrix()
	if err != nil {
		t.Fatal(err)
	}
	var results []Result
	for _, e := range m.Select(Selection{FirstIncrement: true, Identities: []string{IdentitySystem}}) {
		results = append(results, passing(t, e))
	}
	s := Summarize(m, results, testSource, Selection{FirstIncrement: true, Identities: []string{IdentitySystem}})
	if s.Complete || !s.Partial || s.Selected != 11 || s.Passed != 11 || len(s.Omitted) != 57 {
		t.Fatalf("SYSTEM-only first pass: %+v", s)
	}
	if s := Summarize(m, results, testSource, Selection{FirstIncrement: true}); s.Complete || s.Passed != 11 || len(s.Missing) != 9 {
		t.Fatalf("first increment without headless: %+v", s)
	}
}

func TestResultFilesAndSummarizeCommand(t *testing.T) {
	m, err := CaseMatrix()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, r := range allPassing(t, m) {
		if err := WriteResult(dir, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := WriteResult(dir, allPassing(t, m)[0]); err == nil {
		t.Fatal("a record was replaced")
	}
	var out, errOut bytes.Buffer
	if code := Main(nil, []string{"summarize", "--results", dir, "--source", testSource}, &out, &errOut); code != SummaryComplete {
		t.Fatalf("complete exit %d: %s %s", code, out.String(), errOut.String())
	}
	out.Reset()
	if code := Main(nil, []string{"summarize", "--results", dir, "--source", testSource, "--identity", "system"}, &out, &errOut); code != SummaryPartial {
		t.Fatalf("partial exit %d", code)
	}
	if code := Main(nil, []string{"summarize", "--results", dir, "--source", "abcdef0"}, &out, &errOut); code != SummaryFailed {
		t.Fatalf("stale source exit %d", code)
	}
	name := filepath.Join(dir, ResultFileName("N01/assign/system/r1/native-owner"))
	if err := os.Rename(name, filepath.Join(dir, "renamed.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadResults(dir); err == nil {
		t.Fatal("a renamed record was accepted")
	}
	for _, args := range [][]string{
		{"summarize", "--results", "relative", "--source", testSource},
		{"summarize", "--results", dir},
		{"summarize", "--results", dir, "--source", testSource, "--hash"},
		{"matrix", "--hash", "--identity", "system"},
		{"matrix", "--case", "C4"},
	} {
		if code := Main(nil, args, &out, &errOut); code != 2 {
			t.Errorf("%q exit %d", args, code)
		}
	}
}

func TestMainPrintsSelectedMatrix(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Main(nil, []string{"matrix", "--first-increment", "--identity", IdentityHeadless}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 9 {
		t.Fatalf("printed %d executions, want 9", len(lines))
	}
	for _, line := range lines {
		var e Execution
		if err := json.Unmarshal([]byte(line), &e); err != nil || e.Identity != IdentityHeadless || !strings.Contains(e.OwnerRun, "/^headless$") {
			t.Fatalf("line %q: %v", line, err)
		}
	}
	out.Reset()
	if code := Main(nil, []string{"matrix", "--hash"}, &out, &errOut); code != 0 || strings.TrimSpace(out.String()) != MatrixHash() {
		t.Fatalf("hash: %d %q", code, out.String())
	}
	out.Reset()
	if code := Main(nil, []string{"matrix", "--lane", LaneDaemon, "--case", "N06,N07"}, &out, &errOut); code != 0 || strings.Count(out.String(), "\n") != 10 {
		t.Fatalf("daemon lane: %d %q", code, out.String())
	}
	if !slices.Contains(LaunchModes, ModeJobList) {
		t.Fatal("modes")
	}
}
