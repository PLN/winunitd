package nestedjob

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOwnerCommandsAreEnumerated(t *testing.T) {
	main := MainConfig{LaunchMode: ModeJobList, CaseDir: absDir(t), Generation: 1, Work: WorkIdle, OnStop: OnStopCooperative}.Args()
	ok := []Command{
		{Seq: 1, Verb: VerbLaunch, Args: main},
		{Seq: 2, Verb: VerbInUnitJob, PID: 4, Created: 5},
		{Seq: 3, Verb: VerbPIDs},
		{Seq: 4, Verb: VerbStop, TimeoutMS: 5000},
		{Seq: 5, Verb: VerbExit},
	}
	for _, c := range ok {
		if err := ValidateCommand(RoleOwner, c); err != nil {
			t.Errorf("%+v: %v", c, err)
		}
	}
	engine := EngineConfig{CaseDir: absDir(t), Generation: 1, JobHandle: 4, Work: WorkIdle}.Args()
	bad := []Command{
		{Seq: 1, Verb: VerbLaunch},
		{Seq: 1, Verb: VerbLaunch, Args: engine},
		{Seq: 1, Verb: VerbLaunch, Args: []string{`C:\Windows\System32\cmd.exe`, "/c", "exit"}},
		{Seq: 1, Verb: VerbStop},
		{Seq: 1, Verb: VerbStop, TimeoutMS: MaxOwnerStopMS + 1},
		{Seq: 1, Verb: VerbInUnitJob, PID: 4},
		{Seq: 1, Verb: VerbPIDs, Args: main},
		{Seq: 1, Verb: VerbExit, TimeoutMS: 5},
		{Seq: 1, Verb: VerbProbe},
	}
	for _, c := range bad {
		if err := ValidateCommand(RoleOwner, c); err == nil {
			t.Errorf("%+v accepted", c)
		}
	}
	if err := ValidateCommand(RoleMain, Command{Seq: 1, Verb: VerbLaunch, Args: main}); err == nil {
		t.Error("MAIN accepted an owner verb")
	}
}

func TestCommandServerHandlesStrictlyInSequence(t *testing.T) {
	dir := absDir(t)
	var handled []string
	s := &CommandServer{Dir: dir, Role: RoleOwner, Handle: func(c Command) Ack {
		handled = append(handled, c.Verb)
		return Ack{Seq: c.Seq, Verb: c.Verb, OK: true, PIDs: []int{7}}
	}}
	if done, err := s.Poll(); done || err != nil {
		t.Fatalf("empty poll: %v %v", done, err)
	}
	write := func(seq int, c Command) {
		t.Helper()
		if err := WriteJSON(filepath.Join(dir, CommandFile(RoleOwner, seq)), c); err != nil {
			t.Fatal(err)
		}
	}
	ack := func(seq int) Ack {
		t.Helper()
		var a Ack
		if err := ReadJSON(filepath.Join(dir, AckFile(RoleOwner, seq)), &a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	// A later file waits for its predecessor.
	write(2, Command{Seq: 2, Verb: VerbPIDs})
	if done, _ := s.Poll(); done {
		t.Fatal("handled out of order")
	}
	write(1, Command{Seq: 1, Verb: VerbPIDs})
	for i := 0; i < 2; i++ {
		if done, err := s.Poll(); !done || err != nil {
			t.Fatalf("poll %d: %v %v", i, done, err)
		}
	}
	if a := ack(2); !a.OK || len(a.PIDs) != 1 {
		t.Fatalf("ack 2 = %+v", a)
	}
	write(3, Command{Seq: 3, Verb: "shell"})
	write(4, Command{Seq: 9, Verb: VerbPIDs})
	if err := os.WriteFile(filepath.Join(dir, CommandFile(RoleOwner, 5)), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := s.Poll(); err != nil {
			t.Fatal(err)
		}
	}
	for seq := 3; seq <= 5; seq++ {
		if a := ack(seq); a.OK || a.Failure == nil || a.Seq != seq {
			t.Fatalf("rejected command %d acknowledged as %+v", seq, a)
		}
	}
	if len(handled) != 2 {
		t.Fatalf("handler saw %v", handled)
	}
}

func TestRecordCleanupNeedsEveryConfirmation(t *testing.T) {
	r, err := NewRecord("N09", ModeAssign, IdentitySystem, "")
	if err != nil {
		t.Fatal(err)
	}
	r.Cleanup(true)
	r.Cleanup(false)
	r.Cleanup(true)
	if r.CleanupConfirmed {
		t.Fatal("one unconfirmed cleanup was overwritten")
	}
	if r.Exec.Key != "N09/assign/system/r1/native-owner" {
		t.Fatalf("key %s", r.Exec.Key)
	}
	t.Setenv(EnvRepetition, "r2")
	if r, err := NewRecord("N03", ModeJobList, IdentityHeadless, ""); err != nil || r.Exec.Key != "N03/job-list/headless/r2/native-owner" {
		t.Fatalf("repetition key %+v %v", r, err)
	}
	t.Setenv(EnvRepetition, "second")
	if _, err := NewRecord("N03", ModeJobList, IdentityHeadless, ""); err == nil {
		t.Fatal("invalid repetition accepted")
	}
}

func TestRecordFinishWritesBoundRecords(t *testing.T) {
	dir := absDir(t)
	t.Setenv(EnvResults, dir)
	t.Setenv(EnvAdmission, writeTestAdmission(t))
	if !Qualifying() {
		t.Fatal("a run with a results directory is not qualifying")
	}
	r, err := NewRecord("N09", ModeAssign, IdentitySystem, "")
	if err != nil {
		t.Fatal(err)
	}
	tok := &TokenContext{SID: SystemSID, Source: TokenProcess, Elevated: true}
	r.Token = tok
	r.Control("uncapped", ResultPass, "82% of 4 processors", tok)
	r.ControlCleanup("uncapped", true)
	r.Cleanup(true)
	r.Note("capped 24%")
	if err := r.Finish(ResultPass); err != nil {
		t.Fatal(err)
	}
	got, err := ReadResults(dir)
	if err != nil || len(got) != 2 {
		t.Fatalf("records %+v %v", got, err)
	}
	run := testRun(t)
	for _, res := range got {
		if res.Source != testSource || res.Admission != run.Hash || !run.Manifest.Admits(res.Executable) || res.Processors < 1 || !res.CleanupConfirmed {
			t.Fatalf("record is not bound to the admitted run: %+v", res)
		}
	}
	m, err := CaseMatrix()
	if err != nil {
		t.Fatal(err)
	}
	sel := Selection{Cases: []string{"N09"}, Identities: []string{IdentitySystem}, Lanes: []string{LaneOwner}}
	if s := Summarize(m, got, run, sel); s.Passed != 1 || s.Controls != 1 || len(s.Problems) != 0 || s.Selected != 2 {
		t.Fatalf("summary %+v", s)
	}

	// A settings-only pass in a qualifying run is inconclusive, and a
	// control whose cleanup was never confirmed cannot support a pass.
	dir = absDir(t)
	t.Setenv(EnvResults, dir)
	r, _ = NewRecord("N09", ModeJobList, IdentitySystem, "")
	r.Token = tok
	r.Cleanup(true)
	r.MarkInconclusive("not metered")
	if err := r.Finish(ResultPass); err != nil {
		t.Fatal(err)
	}
	r, _ = NewRecord("N09", ModeAssign, IdentitySystem, "")
	r.Token = tok
	r.Control("uncapped", ResultPass, "82%", tok)
	r.ControlCleanup("uncapped", true)
	r.ControlCleanup("uncapped", false)
	r.Cleanup(true)
	if err := r.Finish(ResultPass); err != nil {
		t.Fatal(err)
	}
	got, err = ReadResults(dir)
	if err != nil || len(got) != 3 {
		t.Fatalf("records %+v %v", got, err)
	}
	for _, res := range got {
		switch res.Key {
		case "N09/job-list/system/r1/native-owner":
			if res.Result != ResultInconclusive || !strings.Contains(res.Detail, "not metered") {
				t.Errorf("settings-only record %+v", res)
			}
		case "N09/assign/system/r1/native-owner#uncapped":
			if res.CleanupConfirmed {
				t.Errorf("control cleanup confirmed after a failed confirmation: %+v", res)
			}
		}
	}
	if s := Summarize(m, got, run, sel); s.Passed != 0 || s.Incomplete != 2 {
		t.Fatalf("summary %+v", s)
	}

	// A control that never reported fails; a run without an admission
	// manifest writes its evidence, cannot pass and reports the error.
	dir = absDir(t)
	t.Setenv(EnvResults, dir)
	t.Setenv(EnvAdmission, "")
	r, _ = NewRecord("N09", ModeAssign, IdentitySystem, "")
	r.Token = tok
	r.ControlCleanup("uncapped", true)
	r.Cleanup(true)
	if err := r.Finish(ResultPass); err == nil {
		t.Fatal("a record without an admitted run finished cleanly")
	}
	got, err = ReadResults(dir)
	if err != nil || len(got) != 2 {
		t.Fatalf("records %+v %v", got, err)
	}
	for _, res := range got {
		if res.Admission != "" || res.Result == ResultPass {
			t.Errorf("unadmitted record %+v", res)
		}
	}
	t.Setenv(EnvResults, "")
	if Qualifying() {
		t.Fatal("an ordinary run is qualifying")
	}
	r, _ = NewRecord("N09", ModeAssign, IdentitySystem, "")
	if err := r.Finish(ResultPass); err != nil {
		t.Fatalf("an ordinary run wrote a record: %v", err)
	}

	t.Setenv(EnvHeadlessSID, "S-1-5-21-1-2-3-1001")
	if _, err := HeadlessSID(); err == nil {
		t.Fatal("headless account accepted without the disposable acknowledgement")
	}
	t.Setenv(EnvFixture, "disposable")
	if sid, err := HeadlessSID(); err != nil || sid != "S-1-5-21-1-2-3-1001" {
		t.Fatalf("headless SID %q %v", sid, err)
	}
	t.Setenv(EnvHeadlessSID, SystemSID)
	if _, err := HeadlessSID(); err == nil {
		t.Fatal("SYSTEM accepted as the headless account")
	}
}

func TestManagerOwnerCommandsAreEnumerated(t *testing.T) {
	held := []Identity{{Role: RoleMain, PID: 4, Created: 5}}
	for _, c := range []Command{
		{Seq: 1, Verb: VerbStart}, {Seq: 2, Verb: VerbStopUnit}, {Seq: 3, Verb: VerbInspect},
		{Seq: 4, Verb: VerbInUnitJob, PID: 4, Created: 5}, {Seq: 5, Verb: VerbExpectDrained, Held: held}, {Seq: 6, Verb: VerbExit},
	} {
		if err := ValidateCommand(RoleManagerOwner, c); err != nil {
			t.Errorf("%+v: %v", c, err)
		}
	}
	many := make([]Identity, MaxHeldIdentities+1)
	for i := range many {
		many[i] = Identity{PID: uint32(i + 1), Created: 1}
	}
	for _, c := range []Command{
		{Seq: 1, Verb: VerbExpectDrained},
		{Seq: 1, Verb: VerbExpectDrained, Held: many},
		{Seq: 1, Verb: VerbExpectDrained, Held: []Identity{{PID: 4}}},
		{Seq: 1, Verb: VerbInspect, Held: held},
		{Seq: 1, Verb: VerbStart, Args: []string{"main"}},
		{Seq: 1, Verb: VerbLaunch},
		{Seq: 1, Verb: VerbStop, TimeoutMS: 10},
	} {
		if err := ValidateCommand(RoleManagerOwner, c); err == nil {
			t.Errorf("%+v accepted", c)
		}
	}
	if err := ValidateCommand(RoleOwner, Command{Seq: 1, Verb: VerbInspect}); err == nil {
		t.Error("the runtime owner accepted a manager verb")
	}
}
