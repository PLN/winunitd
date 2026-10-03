package nestedjob

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestClassifyOrdering(t *testing.T) {
	exited := func(role string, at uint64) HeldExit {
		return HeldExit{Role: role, PID: 1, Created: 1, Exited: true, ExitTime: at}
	}
	cases := map[string]struct {
		old  []HeldExit
		want string
	}{
		"ordered":    {[]HeldExit{exited(RoleMain, 90), exited(RoleG1, 99)}, OrderingOrdered},
		"tie":        {[]HeldExit{exited(RoleMain, 90), exited(RoleG2, 100)}, OrderingTie},
		"after":      {[]HeldExit{exited(RoleEngine, 101), exited(RoleG2, 100)}, OrderingAfter},
		"survivor":   {[]HeldExit{exited(RoleMain, 90), {Role: RoleG1, PID: 2, Created: 2}}, OrderingSurvivor},
		"no exit at": {[]HeldExit{{Role: RoleG1, PID: 2, Created: 2, Exited: true}}, OrderingSurvivor},
		"nothing":    {nil, OrderingSurvivor},
	}
	for name, c := range cases {
		if got := ClassifyOrdering(c.old, 100); got.Verdict != c.want {
			t.Errorf("%s: %+v, want %s", name, got, c.want)
		}
	}
	if got := ClassifyOrdering([]HeldExit{exited(RoleMain, 1)}, 0); got.Verdict != OrderingSurvivor {
		t.Errorf("unknown replacement creation: %+v", got)
	}
}

func finishedReport(identity string) ObserverReport {
	sid := SystemSID
	if identity == IdentityHeadless {
		sid = "S-1-5-21-1-2-3-1001"
	}
	tree := []Identity{}
	for _, role := range []string{RoleMain, RoleEngine, RoleG1, RoleG2} {
		tree = append(tree, Identity{Role: role, PID: 10, Created: 200, SID: sid, Generation: 2})
	}
	return ObserverReport{
		Schema: ObserverReportSchema, Stage: StageFinished, Generation: 1, Replacement: 2,
		Old:      []HeldExit{{Role: RoleMain, PID: 1, Created: 1, Exited: true, ExitTime: 150}},
		Ordering: &Ordering{Verdict: OrderingOrdered, ReplacementCreated: 200}, New: tree, CleanupConfirmed: true,
	}
}

func TestDaemonResult(t *testing.T) {
	m, err := CaseMatrix()
	if err != nil {
		t.Fatal(err)
	}
	cfg := RecordConfig{Case: "N07", Mode: ModeJobList, Identity: IdentityHeadless, Repetition: "r2", Source: testSource, TokenSource: TokenS4U, DriverResult: ResultPass}
	r := DaemonResult(m, cfg, finishedReport(IdentityHeadless))
	if r.Result != ResultPass || r.Key != "N07/job-list/headless/r2/immutable-daemon" || r.OwnerProof != "N01/job-list/headless/r1/native-owner" || !r.CleanupConfirmed {
		t.Fatalf("passing result %+v", r)
	}
	mutate := map[string]func(*RecordConfig, *ObserverReport){
		"replaced only": func(_ *RecordConfig, rep *ObserverReport) { rep.Stage = StageReplaced },
		"failed":        func(_ *RecordConfig, rep *ObserverReport) { rep.Stage, rep.Failure = StageFailed, "timeout" },
		"after":         func(_ *RecordConfig, rep *ObserverReport) { rep.Ordering.Verdict = OrderingAfter },
		"tie":           func(_ *RecordConfig, rep *ObserverReport) { rep.Ordering.Verdict = OrderingTie },
		"no ordering":   func(_ *RecordConfig, rep *ObserverReport) { rep.Ordering = nil },
		"no cleanup":    func(_ *RecordConfig, rep *ObserverReport) { rep.CleanupConfirmed = false },
		"mixed tokens":  func(_ *RecordConfig, rep *ObserverReport) { rep.New[2].SID = SystemSID },
		"SYSTEM tree":   func(_ *RecordConfig, rep *ObserverReport) { *rep = finishedReport(IdentitySystem) },
		"process token": func(c *RecordConfig, _ *ObserverReport) { c.TokenSource = TokenProcess },
		"driver failed": func(c *RecordConfig, _ *ObserverReport) { c.DriverResult = ResultFail },
		"not required":  func(c *RecordConfig, _ *ObserverReport) { c.Case = "N05" },
		"system N06": func(c *RecordConfig, _ *ObserverReport) {
			c.Case, c.Identity, c.TokenSource = "N06", IdentitySystem, TokenProcess
		},
		"repeat assign": func(c *RecordConfig, _ *ObserverReport) { c.Mode = ModeAssign },
		"report schema": func(_ *RecordConfig, rep *ObserverReport) { rep.Schema = 2 },
		"elevated tree": func(_ *RecordConfig, rep *ObserverReport) { rep.New[0].Elevated = true },
		"headless in UI": func(_ *RecordConfig, rep *ObserverReport) {
			for i := range rep.New {
				rep.New[i].Session = 1
			}
		},
	}
	for name, f := range mutate {
		c, rep := cfg, finishedReport(IdentityHeadless)
		f(&c, &rep)
		if r := DaemonResult(m, c, rep); r.Result == ResultPass {
			t.Errorf("%s passed: %+v", name, r)
		}
	}
}

func TestObserveAndRecordCommandLines(t *testing.T) {
	dir := absDir(t)
	report := filepath.Join(dir, "observer.json")
	good := []string{"observe", "--case-dir", dir, "--generation", "1", "--replacement", "2", "--crash-pid", "4242",
		"--hold-pid", "77", "--report", report, "--finish-file", filepath.Join(dir, "finish")}
	inv, err := Parse(good)
	if err != nil || inv.Observe == nil || inv.Observe.CrashPID != 4242 || inv.Observe.CrashImage != "winunitd.exe" || len(inv.Observe.HoldPIDs) != 1 {
		t.Fatalf("observe: %+v %v", inv.Observe, err)
	}
	replace := func(flag, value string) []string {
		out := append([]string(nil), good...)
		for i := range out {
			if out[i] == flag {
				out[i+1] = value
			}
		}
		return out
	}
	for name, args := range map[string][]string{
		"same generation":  replace("--replacement", "1"),
		"no crash target":  replace("--crash-pid", "0"),
		"crash image":      append(append([]string(nil), good...), "--crash-image", `C:\x\winunitd.exe`),
		"relative report":  replace("--report", "observer.json"),
		"long timeout":     append(append([]string(nil), good...), "--timeout", "1h"),
		"hold pid":         replace("--hold-pid", "x"),
		"relative casedir": replace("--case-dir", "case"),
	} {
		if _, err := Parse(args); err == nil {
			t.Errorf("observe %s accepted", name)
		}
	}
	results := absDir(t)
	if err := WriteJSON(report, finishedReport(IdentityHeadless)); err != nil {
		t.Fatal(err)
	}
	record := []string{"record", "--report", report, "--results", results, "--case", "N06", "--mode", ModeJobList,
		"--identity", IdentityHeadless, "--repetition", "r3", "--source", testSource, "--token-source", TokenS4U, "--driver-result", ResultPass}
	var out, errOut bytes.Buffer
	if code := Main(nil, record, &out, &errOut); code != 0 {
		t.Fatalf("record exit %d: %s", code, errOut.String())
	}
	got, err := ReadResults(results)
	if err != nil || len(got) != 1 || got[0].Key != "N06/job-list/headless/r3/immutable-daemon" || got[0].Result != ResultPass {
		t.Fatalf("recorded %+v %v", got, err)
	}
	// The daemon record needs its owner proof to count.
	m, err := CaseMatrix()
	if err != nil {
		t.Fatal(err)
	}
	sel := Selection{Cases: []string{"N06"}}
	if s := Summarize(m, got, testSource, sel); s.Passed != 0 || len(s.Problems) == 0 {
		t.Fatalf("daemon record without owner proof: %+v", s)
	}
	proof := passing(t, m.Select(Selection{Cases: []string{"N01"}, Identities: []string{IdentityHeadless}})[1])
	if proof.Key != "N01/job-list/headless/r1/native-owner" {
		t.Fatalf("proof key %s", proof.Key)
	}
	if s := Summarize(m, append(got, proof), testSource, sel); s.Passed != 1 {
		t.Fatalf("daemon record with owner proof: %+v", s)
	}
	for name, args := range map[string][]string{
		"driver result": append(append([]string(nil), record...), "--driver-result", "maybe"),
		"multi-line":    append(append([]string(nil), record...), "--detail", "a\nb"),
		"token source":  append(append([]string(nil), record...), "--token-source", "wts"),
		"no source":     append(append([]string(nil), record[:13]...), record[15:]...),
	} {
		if _, err := Parse(args); err == nil {
			t.Errorf("record %s accepted", name)
		}
	}
	failed := append(append([]string(nil), record...), "--driver-result", ResultFail)
	failed[4] = absDir(t)
	if code := Main(nil, failed, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "driver checks failed") {
		t.Fatalf("failed record exit %d: %s", code, errOut.String())
	}
}
