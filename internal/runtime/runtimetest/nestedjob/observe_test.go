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

// testBinding is the command-line binding of a daemon case.
func testBinding(caseID, mode, identity, repetition string) DaemonBinding {
	sid := SystemSID
	if identity == IdentityHeadless {
		sid = testHeadlessSID
	}
	return DaemonBinding{Case: caseID, Mode: mode, Identity: identity, Repetition: repetition, ExpectSID: sid, CrashRole: ExpectedCrashRole(caseID)}
}

// daemonTree is one generation's realistic tree: distinct processes,
// parent links, the binding's account and the admitted fixture image.
func daemonTree(b DaemonBinding, gen int, pid uint32, created uint64) []Identity {
	var ids []Identity
	for i, role := range []string{RoleMain, RoleEngine, RoleG1, RoleG2} {
		ids = append(ids, Identity{Role: role, PID: pid + uint32(4*i), Created: created + uint64(10*i),
			Image: `C:\fixture\nested-job.exe`, ImageSHA256: testFixtureHash, SID: b.ExpectSID,
			Elevated: b.Identity == IdentitySystem, Invocation: "inv", Generation: gen})
	}
	ids[1].ParentPID, ids[1].ParentCreated = ids[0].PID, ids[0].Created
	for i := 2; i < 4; i++ {
		ids[i].ParentPID, ids[i].ParentCreated = ids[1].PID, ids[1].Created
	}
	return ids
}

// daemonReport is a finished, valid observer report for a bound binding:
// the crash at 3000, old exits from 3010, the replacement MAIN at 5000.
func daemonReport(b DaemonBinding) ObserverReport {
	crashSID := b.ExpectSID
	if b.CrashRole == CrashBroker {
		crashSID = SystemSID
	}
	rep := ObserverReport{
		Schema: ObserverReportSchema, Stage: StageFinished, Binding: b, Generation: 1, Replacement: 2,
		OldTree: daemonTree(b, 1, 1000, 1000), NewTree: daemonTree(b, 2, 2000, 5000),
		Crash:   &HeldExit{Role: b.CrashRole, PID: 500, Created: 500, Image: DaemonImage, SID: crashSID, Exited: true, ExitTime: 3010},
		CrashAt: 3000, ReplacementCreated: 5000, CleanupConfirmed: true,
	}
	for i, id := range rep.OldTree {
		rep.Old = append(rep.Old, HeldExit{Role: id.Role, PID: id.PID, Created: id.Created, SID: id.SID, Exited: true, ExitTime: 3100 + uint64(i)})
		rep.EntryObservation = append(rep.EntryObservation, PreviousCheck{Role: id.Role, PID: id.PID, Created: id.Created, State: PreviousExited})
	}
	if b.heldManagers() == 1 {
		m := HeldExit{Role: CrashUserManager, PID: 600, Created: 600, Image: DaemonImage, SID: b.ExpectSID, Exited: true, ExitTime: 3050}
		rep.Managers = []HeldExit{m}
		rep.Old = append(rep.Old, m)
	}
	rep.Old = append(rep.Old, *rep.Crash)
	o := ClassifyOrdering(rep.Old, rep.ReplacementCreated)
	rep.Ordering = &o
	return rep
}

func TestValidateDaemonReport(t *testing.T) {
	run := testRun(t)
	for _, b := range []DaemonBinding{
		testBinding("N06", ModeAssign, IdentityHeadless, "r1"),
		testBinding("N07", ModeJobList, IdentityHeadless, "r2"),
		testBinding("N07", ModeAssign, IdentitySystem, "r1"),
	} {
		b = b.bind(run)
		if err := ValidateDaemonReport(daemonReport(b), b, run.Manifest); err != nil {
			t.Fatalf("%s %s: %v", b.Case, b.Identity, err)
		}
	}
	want := testBinding("N07", ModeJobList, IdentityHeadless, "r2").bind(run)
	removeOld := func(rep *ObserverReport, pid uint32) {
		for i, h := range rep.Old {
			if h.PID == pid {
				rep.Old = append(rep.Old[:i:i], rep.Old[i+1:]...)
				return
			}
		}
	}
	mutations := map[string]func(*ObserverReport, *DaemonBinding){
		"schema":       func(r *ObserverReport, _ *DaemonBinding) { r.Schema = 1 },
		"replaced":     func(r *ObserverReport, _ *DaemonBinding) { r.Stage = StageReplaced },
		"failed":       func(r *ObserverReport, _ *DaemonBinding) { r.Stage, r.Failure = StageFailed, "timeout" },
		"no cleanup":   func(r *ObserverReport, _ *DaemonBinding) { r.CleanupConfirmed = false },
		"missing role": func(r *ObserverReport, _ *DaemonBinding) { r.NewTree = r.NewTree[:3] },
		"missing old role": func(r *ObserverReport, _ *DaemonBinding) {
			removeOld(r, r.OldTree[3].PID)
			r.OldTree = r.OldTree[:3]
		},
		"tree order": func(r *ObserverReport, _ *DaemonBinding) { r.NewTree[2], r.NewTree[3] = r.NewTree[3], r.NewTree[2] },
		"duplicate identity": func(r *ObserverReport, _ *DaemonBinding) {
			r.NewTree[3].PID, r.NewTree[3].Created = r.NewTree[2].PID, r.NewTree[2].Created
		},
		"all four roles one process": func(r *ObserverReport, _ *DaemonBinding) {
			for i := range r.NewTree {
				r.NewTree[i].PID, r.NewTree[i].Created = 10, 5000
				r.NewTree[i].ParentPID, r.NewTree[i].ParentCreated = 10, 5000
			}
		},
		"old process reused": func(r *ObserverReport, _ *DaemonBinding) {
			r.NewTree[2].PID, r.NewTree[2].Created = r.OldTree[2].PID, r.OldTree[2].Created
		},
		"broken parent link": func(r *ObserverReport, _ *DaemonBinding) { r.NewTree[3].ParentPID = r.NewTree[0].PID },
		"wrong generation":   func(r *ObserverReport, _ *DaemonBinding) { r.Replacement = 3 },
		"tree generation":    func(r *ObserverReport, _ *DaemonBinding) { r.NewTree[1].Generation = 3 },
		"old tree generation": func(r *ObserverReport, _ *DaemonBinding) {
			r.OldTree[1].Generation = 2
		},
		"wrong account": func(r *ObserverReport, _ *DaemonBinding) {
			for i := range r.NewTree {
				r.NewTree[i].SID = "S-1-5-21-1-2-3-1002"
			}
		},
		"elevated headless": func(r *ObserverReport, _ *DaemonBinding) { r.NewTree[0].Elevated = true },
		"interactive session": func(r *ObserverReport, _ *DaemonBinding) {
			for i := range r.NewTree {
				r.NewTree[i].Session = 1
			}
		},
		"unadmitted image": func(r *ObserverReport, _ *DaemonBinding) { r.NewTree[2].ImageSHA256 = strings.Repeat("cd", 32) },
		"no crash":         func(r *ObserverReport, _ *DaemonBinding) { removeOld(r, r.Crash.PID); r.Crash = nil },
		"crash role":       func(r *ObserverReport, _ *DaemonBinding) { r.Crash.Role = CrashUserManager },
		"crash image":      func(r *ObserverReport, _ *DaemonBinding) { r.Crash.Image = "nested-job.exe" },
		"crash account":    func(r *ObserverReport, _ *DaemonBinding) { r.Crash.SID = testHeadlessSID },
		"crash not in old": func(r *ObserverReport, _ *DaemonBinding) { removeOld(r, r.Crash.PID) },
		"crash record":     func(r *ObserverReport, _ *DaemonBinding) { r.Crash.ExitTime = 3011 },
		"crash after exit": func(r *ObserverReport, _ *DaemonBinding) { r.CrashAt = 3020 },
		"no crash time":    func(r *ObserverReport, _ *DaemonBinding) { r.CrashAt = 0 },
		"old list removed": func(r *ObserverReport, _ *DaemonBinding) { r.Old = nil },
		"old list only MAIN": func(r *ObserverReport, _ *DaemonBinding) {
			r.Old = r.Old[:1]
		},
		"unknown old process": func(r *ObserverReport, _ *DaemonBinding) {
			r.Old = append(r.Old, HeldExit{Role: RoleProbe, PID: 9, Created: 9, Exited: true, ExitTime: 3100})
		},
		"old survivor": func(r *ObserverReport, _ *DaemonBinding) { r.Old[1].Exited, r.Old[1].ExitTime = false, 0 },
		"exit after":   func(r *ObserverReport, _ *DaemonBinding) { r.Old[2].ExitTime = 6000 },
		"exit tie":     func(r *ObserverReport, _ *DaemonBinding) { r.Old[2].ExitTime = 5000 },
		"stale verdict": func(r *ObserverReport, _ *DaemonBinding) {
			r.Old[0].ExitTime = 7000
			r.Ordering.Verdict = OrderingOrdered
		},
		"replacement time": func(r *ObserverReport, _ *DaemonBinding) {
			r.ReplacementCreated = 4999
		},
		"replacement before crash": func(r *ObserverReport, _ *DaemonBinding) {
			r.CrashAt, r.Crash.ExitTime, r.Old[len(r.Old)-1].ExitTime = 6000, 6001, 6001
		},
		"no manager": func(r *ObserverReport, _ *DaemonBinding) {
			removeOld(r, r.Managers[0].PID)
			r.Managers = nil
		},
		"manager account": func(r *ObserverReport, _ *DaemonBinding) {
			r.Managers[0].SID = SystemSID
		},
		"manager record":     func(r *ObserverReport, _ *DaemonBinding) { r.Managers[0].ExitTime = 3051 },
		"no entry check":     func(r *ObserverReport, _ *DaemonBinding) { r.EntryObservation = nil },
		"entry saw survivor": func(r *ObserverReport, _ *DaemonBinding) { r.EntryObservation[1].State = PreviousRunning },
		"relabeled case": func(_ *ObserverReport, b *DaemonBinding) {
			b.Case, b.CrashRole = "N06", CrashUserManager
		},
		"relabeled mode":       func(_ *ObserverReport, b *DaemonBinding) { b.Mode = ModeAssign },
		"relabeled repetition": func(_ *ObserverReport, b *DaemonBinding) { b.Repetition = "r3" },
		"other account":        func(_ *ObserverReport, b *DaemonBinding) { b.ExpectSID = "S-1-5-21-1-2-3-1002" },
		"other admission":      func(_ *ObserverReport, b *DaemonBinding) { b.Admission = strings.Repeat("1", 64) },
		"other source":         func(_ *ObserverReport, b *DaemonBinding) { b.Source = strings.Repeat("f", 40) },
	}
	for name, mutate := range mutations {
		rep, b := daemonReport(want), want
		mutate(&rep, &b)
		if err := ValidateDaemonReport(rep, b, run.Manifest); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := ValidateDaemonReport(daemonReport(want), want, nil); err == nil {
		t.Error("a report without an admitted run accepted")
	}
	n06 := testBinding("N06", ModeAssign, IdentityHeadless, "r1").bind(run)
	extra := daemonReport(n06)
	extra.Managers = daemonReport(want).Managers
	extra.Old = append(extra.Old, extra.Managers...)
	if err := ValidateDaemonReport(extra, n06, run.Manifest); err == nil {
		t.Error("N06 accepted a held broker-lane manager")
	}
	for _, b := range []DaemonBinding{
		testBinding("N06", ModeAssign, IdentitySystem, "r1"),
		testBinding("N05", ModeAssign, IdentityHeadless, "r1"),
		testBinding("N07", "fallback", IdentityHeadless, "r1"),
		testBinding("N07", ModeAssign, IdentityHeadless, "r0"),
		{Case: "N07", Mode: ModeAssign, Identity: IdentitySystem, Repetition: "r1", ExpectSID: testHeadlessSID, CrashRole: CrashBroker},
		{Case: "N07", Mode: ModeAssign, Identity: IdentityHeadless, Repetition: "r1", ExpectSID: SystemSID, CrashRole: CrashBroker},
		{Case: "N07", Mode: ModeAssign, Identity: IdentityHeadless, Repetition: "r1", ExpectSID: testHeadlessSID, CrashRole: CrashUserManager},
	} {
		b = b.bind(run)
		if err := ValidateDaemonReport(daemonReport(b), b, run.Manifest); err == nil {
			t.Errorf("binding %+v accepted", b)
		}
	}
}

func TestDaemonResult(t *testing.T) {
	m, err := CaseMatrix()
	if err != nil {
		t.Fatal(err)
	}
	run := testRun(t)
	cfg := RecordConfig{Binding: testBinding("N07", ModeJobList, IdentityHeadless, "r2"), DriverResult: ResultPass}
	rep := daemonReport(cfg.Binding.bind(run))
	r := DaemonResult(m, cfg, rep, run, testFixtureHash)
	if r.Result != ResultPass || r.Key != "N07/job-list/headless/r2/immutable-daemon" || r.OwnerProof != "N01/job-list/headless/r1/native-owner" ||
		!r.CleanupConfirmed || r.Admission != run.Hash || r.Source != testSource || IdentityOf(r.Token) != IdentityHeadless || r.Token.SID != testHeadlessSID {
		t.Fatalf("passing result %+v", r)
	}
	if r := DaemonResult(m, cfg, rep, run, strings.Repeat("cd", 32)); r.Result == ResultPass {
		t.Errorf("an unadmitted recorder passed: %+v", r)
	}
	failed := cfg
	failed.DriverResult = ResultFail
	if r := DaemonResult(m, failed, rep, run, testFixtureHash); r.Result == ResultPass || !strings.Contains(r.Detail, "driver checks failed") {
		t.Errorf("driver failure passed: %+v", r)
	}
	relabeled := cfg
	relabeled.Binding.Repetition = "r3"
	if r := DaemonResult(m, relabeled, rep, run, testFixtureHash); r.Result == ResultPass || r.Token != nil || r.CleanupConfirmed {
		t.Errorf("a relabeled report passed: %+v", r)
	}
	other := run
	other.Hash = strings.Repeat("2", 64)
	if r := DaemonResult(m, cfg, rep, other, testFixtureHash); r.Result == ResultPass {
		t.Errorf("another admitted run passed: %+v", r)
	}
	unrequired := cfg
	unrequired.Binding = testBinding("N07", ModeAssign, IdentityHeadless, "r3")
	if r := DaemonResult(m, unrequired, daemonReport(unrequired.Binding.bind(run)), run, testFixtureHash); r.Result == ResultPass {
		t.Errorf("an execution outside the matrix passed: %+v", r)
	}
}

func TestObserveAndRecordCommandLines(t *testing.T) {
	dir := absDir(t)
	admission := writeTestAdmission(t)
	report := filepath.Join(dir, "observer.json")
	good := []string{"observe", "--case-dir", dir, "--generation", "1", "--crash-pid", "4242", "--hold-pid", "77",
		"--report", report, "--finish-file", filepath.Join(dir, "finish"), "--admission", admission,
		"--case", "N07", "--mode", ModeJobList, "--identity", IdentityHeadless, "--repetition", "r2", "--expect-sid", testHeadlessSID}
	inv, err := Parse(good)
	if err != nil || inv.Observe == nil || inv.Observe.CrashPID != 4242 || inv.Observe.Replacement != 2 || len(inv.Observe.HoldPIDs) != 1 ||
		inv.Observe.Binding.CrashRole != CrashBroker || inv.Observe.Binding.Source != "" {
		t.Fatalf("observe: %+v %v", inv.Observe, err)
	}
	replace := func(args []string, flag, value string) []string {
		out := append([]string(nil), args...)
		for i := range out {
			if out[i] == flag {
				out[i+1] = value
			}
		}
		return out
	}
	without := func(args []string, flag string) []string {
		var out []string
		for i := 0; i < len(args); i++ {
			if args[i] == flag {
				i++
				continue
			}
			out = append(out, args[i])
		}
		return out
	}
	n06 := replace(without(good, "--hold-pid"), "--case", "N06")
	if inv, err := Parse(n06); err != nil || inv.Observe.Binding.CrashRole != CrashUserManager {
		t.Fatalf("N06 observe: %+v %v", inv.Observe, err)
	}
	for name, args := range map[string][]string{
		"replacement flag":     append(append([]string(nil), good...), "--replacement", "3"),
		"no crash target":      replace(good, "--crash-pid", "0"),
		"crash image flag":     append(append([]string(nil), good...), "--crash-image", "winunitd.exe"),
		"source flag":          append(append([]string(nil), good...), "--source", testSource),
		"relative report":      replace(good, "--report", "observer.json"),
		"relative admission":   replace(good, "--admission", "admission.json"),
		"no admission":         without(good, "--admission"),
		"long timeout":         append(append([]string(nil), good...), "--timeout", "1h"),
		"hold pid":             replace(good, "--hold-pid", "x"),
		"no held manager":      without(good, "--hold-pid"),
		"two held managers":    append(append([]string(nil), good...), "--hold-pid", "78"),
		"N06 with manager":     replace(good, "--case", "N06"),
		"relative casedir":     replace(good, "--case-dir", "case"),
		"primary case":         replace(good, "--case", "N05"),
		"SYSTEM N06":           replace(replace(n06, "--identity", IdentitySystem), "--expect-sid", SystemSID),
		"SYSTEM headless SID":  replace(without(good, "--hold-pid"), "--identity", IdentitySystem),
		"headless SYSTEM SID":  replace(good, "--expect-sid", SystemSID),
		"no expected account":  without(good, "--expect-sid"),
		"repetition":           replace(good, "--repetition", "second"),
		"mode":                 replace(good, "--mode", "fallback"),
		"generation exhausted": replace(good, "--generation", "0"),
	} {
		if _, err := Parse(args); err == nil {
			t.Errorf("observe %s accepted", name)
		}
	}

	results := absDir(t)
	run := testRun(t)
	if err := WriteJSON(report, daemonReport(testBinding("N07", ModeJobList, IdentityHeadless, "r2").bind(run))); err != nil {
		t.Fatal(err)
	}
	record := []string{"record", "--report", report, "--results", results, "--admission", admission, "--case", "N07",
		"--mode", ModeJobList, "--identity", IdentityHeadless, "--repetition", "r2", "--expect-sid", testHeadlessSID, "--driver-result", ResultPass}
	var out, errOut bytes.Buffer
	if code := Main(nil, record, &out, &errOut); code != 0 {
		t.Fatalf("record exit %d: %s", code, errOut.String())
	}
	got, err := ReadResults(results)
	if err != nil || len(got) != 1 || got[0].Key != "N07/job-list/headless/r2/immutable-daemon" || got[0].Result != ResultPass {
		t.Fatalf("recorded %+v %v", got, err)
	}
	// The daemon record needs its owner proof, under the same account.
	m, err := CaseMatrix()
	if err != nil {
		t.Fatal(err)
	}
	sel := Selection{Cases: []string{"N07"}, Identities: []string{IdentityHeadless}, Lanes: []string{LaneDaemon}}
	if s := Summarize(m, got, run, sel); s.Passed != 0 || len(s.Problems) == 0 {
		t.Fatalf("daemon record without owner proof: %+v", s)
	}
	proof := passing(t, m.Select(Selection{Cases: []string{"N01"}, Identities: []string{IdentityHeadless}})[1])
	if proof.Key != "N01/job-list/headless/r1/native-owner" {
		t.Fatalf("proof key %s", proof.Key)
	}
	if s := Summarize(m, append(got, proof), run, sel); s.Passed != 1 {
		t.Fatalf("daemon record with owner proof: %+v", s)
	}
	proof.Token.SID = "S-1-5-21-1-2-3-1002"
	if s := Summarize(m, append(got, proof), run, sel); s.Passed != 0 {
		t.Fatalf("owner proof of another account: %+v", s)
	}
	for name, args := range map[string][]string{
		"driver result": replace(record, "--driver-result", "maybe"),
		"multi-line":    append(append([]string(nil), record...), "--detail", "a\nb"),
		"token source":  append(append([]string(nil), record...), "--token-source", TokenS4U),
		"source flag":   append(append([]string(nil), record...), "--source", testSource),
		"no admission":  without(record, "--admission"),
		"no account":    without(record, "--expect-sid"),
		"case dir":      append(append([]string(nil), record...), "--case-dir", dir),
	} {
		if _, err := Parse(args); err == nil {
			t.Errorf("record %s accepted", name)
		}
	}
	// A report relabeled on the command line is recorded as a failure.
	for name, args := range map[string][]string{
		"driver failed": replace(record, "--driver-result", ResultFail),
		"relabeled":     replace(record, "--repetition", "r3"),
		"other account": replace(record, "--expect-sid", "S-1-5-21-1-2-3-1002"),
	} {
		args = replace(args, "--results", absDir(t))
		errOut.Reset()
		if code := Main(nil, args, &out, &errOut); code != 1 {
			t.Errorf("%s record exit %d: %s", name, code, errOut.String())
		}
	}
}
