package nestedjob

import (
	"os"
	"path/filepath"
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
	t.Setenv(EnvSource, testSource)
	r, err := NewRecord("N09", ModeAssign, IdentitySystem, "")
	if err != nil {
		t.Fatal(err)
	}
	r.Token = &TokenContext{SID: SystemSID, Source: TokenProcess}
	r.Cleanup(true)
	r.Control("uncapped", ResultPass, "82% of 4 processors")
	r.Note("capped 24%")
	if err := r.Finish(ResultPass); err != nil {
		t.Fatal(err)
	}
	got, err := ReadResults(dir)
	if err != nil || len(got) != 2 {
		t.Fatalf("records %+v %v", got, err)
	}
	m, err := CaseMatrix()
	if err != nil {
		t.Fatal(err)
	}
	s := Summarize(m, got, testSource, Selection{Cases: []string{"N09"}, Identities: []string{IdentitySystem}, Lanes: []string{LaneOwner}})
	if s.Passed != 1 || s.Controls != 1 || len(s.Problems) != 0 || s.Selected != 2 {
		t.Fatalf("summary %+v", s)
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
