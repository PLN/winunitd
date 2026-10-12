package nestedjob

import (
	"bytes"
	"fmt"
	"os"
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
// parent links, the binding's account and the fixture image.
func daemonTree(b DaemonBinding, gen int, pid uint32, created uint64, image string) []Identity {
	var ids []Identity
	for i, role := range []string{RoleMain, RoleEngine, RoleG1, RoleG2} {
		ids = append(ids, Identity{Role: role, PID: pid + uint32(4*i), Created: created + uint64(10*i),
			Image: `C:\fixture\nested-job.exe`, ImageSHA256: image, SID: b.ExpectSID,
			Elevated: b.Identity == IdentitySystem, Invocation: fmt.Sprintf("inv-%d", gen), Generation: gen})
	}
	ids[1].ParentPID, ids[1].ParentCreated = ids[0].PID, ids[0].Created
	for i := 2; i < 4; i++ {
		ids[i].ParentPID, ids[i].ParentCreated = ids[1].PID, ids[1].Created
	}
	return ids
}

// generationEvents is a generation's complete MAIN report in mode: start,
// entry check, inner job, the mode's launch sequence, tree and READY.
func generationEvents(mode string, ids []Identity, gen int, previous []PreviousCheck) []Event {
	main := ids[0]
	created := Identity{Role: RoleEngine, PID: ids[1].PID, Created: ids[1].Created, ParentPID: main.PID, ParentCreated: main.Created, Generation: gen}
	inner := &InnerJob{Handle: 0x1a4, LimitFlags: jobLimitKillOnJobClose}
	members := []Member{{Identity: ids[0]}}
	for _, id := range ids[1:] {
		members = append(members, Member{Identity: id, InInner: true})
	}
	prev := Event{Kind: EventPrevious, Previous: previous}
	if previous == nil {
		prev.Note = "none"
	}
	events := []Event{{Kind: EventStart, Mode: mode, OS: "10.0.20348", Identity: &main}, prev,
		{Kind: EventInnerJob, Inner: inner}, {Kind: EventCreated, Identity: &created}}
	if mode == ModeAssign {
		events = append(events, Event{Kind: EventAssigned, Identity: &created})
	}
	events = append(events, Event{Kind: EventResumed, Identity: &created}, Event{Kind: EventTree, Tree: members, Inner: inner},
		Event{Kind: EventCommand, Note: "observed 1"}, Event{Kind: EventReady})
	return sequenced(events, gen)
}

// sequenced numbers events as MAIN's report writer does.
func sequenced(events []Event, gen int) []Event {
	for i := range events {
		events[i].Seq, events[i].Generation, events[i].Time = i+1, gen, "2026-10-03T00:00:00Z"
	}
	return events
}

// daemonReport is a finished, valid observer report for a bound binding:
// the crash at 3000, old exits from 3010, the replacement MAIN at 5000.
func daemonReport(b DaemonBinding) ObserverReport { return daemonReportImage(b, testFixtureHash) }

func daemonReportImage(b DaemonBinding, image string) ObserverReport {
	crashSID := b.ExpectSID
	if b.CrashRole == CrashBroker {
		crashSID = SystemSID
	}
	rep := ObserverReport{
		Schema: ObserverReportSchema, Stage: StageFinished, Binding: b, Generation: 1, Replacement: 2,
		OldTree: daemonTree(b, 1, 1000, 1000, image), NewTree: daemonTree(b, 2, 2000, 5000, image),
		Crash:   &HeldExit{Role: b.CrashRole, PID: 500, Created: 500, Image: DaemonImage, SID: crashSID, Exited: true, ExitTime: 3010},
		CrashAt: 3000, ReplacementCreated: 5000, CleanupConfirmed: true,
	}
	var entry []PreviousCheck
	for i, id := range rep.OldTree {
		rep.Old = append(rep.Old, HeldExit{Role: id.Role, PID: id.PID, Created: id.Created, SID: id.SID, Exited: true, ExitTime: 3100 + uint64(i)})
		entry = append(entry, PreviousCheck{Role: id.Role, PID: id.PID, Created: id.Created, State: PreviousExited})
	}
	rep.OldEvents = generationEvents(b.Mode, rep.OldTree, 1, nil)
	rep.NewEvents = generationEvents(b.Mode, rep.NewTree, 2, entry)
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
		"no entry check":     func(r *ObserverReport, _ *DaemonBinding) { entryChecks(r).Previous = nil },
		"entry saw survivor": func(r *ObserverReport, _ *DaemonBinding) { entryChecks(r).Previous[1].State = PreviousRunning },
		"entry check failed": func(r *ObserverReport, _ *DaemonBinding) {
			entryChecks(r).Previous[1].Error = "access denied"
		},
		"entry checks repeated": func(r *ObserverReport, _ *DaemonBinding) {
			p := entryChecks(r).Previous
			p[3] = p[2]
		},
		"entry check of an unrelated process": func(r *ObserverReport, _ *DaemonBinding) {
			entryChecks(r).Previous[2].PID = 9999
		},
		"entry check of a reused creation time": func(r *ObserverReport, _ *DaemonBinding) {
			entryChecks(r).Previous[0].Created++
		},
		"entry checks of the new tree": func(r *ObserverReport, _ *DaemonBinding) {
			p := entryChecks(r).Previous
			for i, id := range r.NewTree {
				p[i] = PreviousCheck{Role: id.Role, PID: id.PID, Created: id.Created, State: PreviousExited}
			}
		},
		// The raw proof of one mode relabeled as the other, both labels changed.
		"raw proof relabeled to the other mode": func(r *ObserverReport, b *DaemonBinding) { r.Binding.Mode, b.Mode = ModeAssign, ModeAssign },
		"assignment in the job-list sequence": func(r *ObserverReport, _ *DaemonBinding) {
			created := *r.NewEvents[3].Identity
			events := append(append([]Event(nil), r.NewEvents[:4]...), Event{Kind: EventAssigned, Identity: &created})
			r.NewEvents = sequenced(append(events, r.NewEvents[4:]...), 2)
		},
		"inner job with breakaway": func(r *ObserverReport, _ *DaemonBinding) {
			inner := *r.OldEvents[2].Inner
			inner.LimitFlags |= jobLimitBreakawayOK
			r.OldEvents[2].Inner, r.OldEvents[eventIndex(r.OldEvents, EventTree)].Inner = &inner, &inner
		},
		"no old report":         func(r *ObserverReport, _ *DaemonBinding) { r.OldEvents = nil },
		"no replacement report": func(r *ObserverReport, _ *DaemonBinding) { r.NewEvents = nil },
		"truncated old report": func(r *ObserverReport, _ *DaemonBinding) {
			r.OldEvents = sequenced(append(r.OldEvents, Event{Kind: EventTruncated}), 1)
		},
		"report of another generation": func(r *ObserverReport, _ *DaemonBinding) { r.NewEvents = sequenced(r.NewEvents, 3) },
		"report sequence gap":          func(r *ObserverReport, _ *DaemonBinding) { r.OldEvents[4].Seq = 9 },
		"fatal replacement report": func(r *ObserverReport, _ *DaemonBinding) {
			r.NewEvents = sequenced(append(r.NewEvents, Event{Kind: EventFatal, Failure: &Failure{Op: "x", Message: "y"}}), 2)
		},
		"report tree is not the held tree": func(r *ObserverReport, _ *DaemonBinding) {
			r.OldEvents[eventIndex(r.OldEvents, EventTree)].Tree[2].PID = 7777
		},
		"daemon image as workload": func(r *ObserverReport, _ *DaemonBinding) {
			*r = daemonReportImage(r.Binding, testExecutableHash(t))
		},
		"relabeled case": func(_ *ObserverReport, b *DaemonBinding) {
			b.Case, b.CrashRole = "N06", CrashUserManager
		},
		"recorder relabels mode": func(_ *ObserverReport, b *DaemonBinding) { b.Mode = ModeAssign },
		"relabeled repetition":   func(_ *ObserverReport, b *DaemonBinding) { b.Repetition = "r3" },
		"other account":          func(_ *ObserverReport, b *DaemonBinding) { b.ExpectSID = "S-1-5-21-1-2-3-1002" },
		"other admission":        func(_ *ObserverReport, b *DaemonBinding) { b.Admission = strings.Repeat("1", 64) },
		"other source":           func(_ *ObserverReport, b *DaemonBinding) { b.Source = strings.Repeat("f", 40) },
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

func eventIndex(events []Event, kind string) int {
	for i, e := range events {
		if e.Kind == kind {
			return i
		}
	}
	return -1
}

// entryChecks is the replacement report's entry check event.
func entryChecks(r *ObserverReport) *Event {
	for i := range r.NewEvents {
		if r.NewEvents[i].Kind == EventPrevious {
			return &r.NewEvents[i]
		}
	}
	return nil
}

func testExecutableHash(t *testing.T) string {
	t.Helper()
	sum, err := ExecutableSHA256()
	if err != nil {
		t.Fatal(err)
	}
	return sum
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
	// Admitted, but not the fixture: another role's executable.
	if r := DaemonResult(m, cfg, rep, run, testExecutableHash(t)); r.Result == ResultPass || !strings.Contains(r.Detail, "other than the admitted fixture") {
		t.Errorf("a recorder other than the fixture passed: %+v", r)
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

// writeFixtureAdmission admits this test binary as the fixture, so the
// record command running in it is the admitted recorder.
func writeFixtureAdmission(t *testing.T) (string, AdmittedRun) {
	t.Helper()
	data := []byte(fmt.Sprintf(`{"schema":1,"source":%q,"dirty":false,"artifacts":[{"name":%q,"sha256":%q}]}`,
		testSource, FixtureArtifact, testExecutableHash(t)))
	path := filepath.Join(absDir(t), "fixture-admission.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	run, err := DecodeAdmission(data)
	if err != nil {
		t.Fatal(err)
	}
	return path, run
}

func TestObserveAndRecordCommandLines(t *testing.T) {
	dir := absDir(t)
	admission, fixtureRun := writeFixtureAdmission(t)
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
	run := fixtureRun
	if err := WriteJSON(report, daemonReportImage(testBinding("N07", ModeJobList, IdentityHeadless, "r2").bind(run), testExecutableHash(t))); err != nil {
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
	proof.Admission, proof.Executable = run.Hash, testExecutableHash(t)
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
