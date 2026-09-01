package timers

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testEngine(t *testing.T, fire FireFunc) (*Engine, *Fake) {
	t.Helper()
	fk := NewFake(time.Time{})
	e := NewEngine(fk.Clock(), nil, fire)
	t.Cleanup(e.Stop)
	return e, fk
}

func TestStoreRoundTrip(t *testing.T) {
	t.Parallel()
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Load("missing.timer"); !got.LastActual.IsZero() {
		t.Fatalf("missing = %+v", got)
	}
	want := Runtime{
		LastScheduled: time.Date(2026, 8, 30, 3, 0, 0, 0, time.UTC),
		LastActual:    time.Date(2026, 8, 30, 3, 0, 1, 0, time.UTC),
		LastSuccess:   time.Date(2026, 8, 30, 3, 0, 2, 0, time.UTC),
	}
	if err := s.Save("foo.timer", want); err != nil {
		t.Fatal(err)
	}
	got := s.Load("foo.timer")
	if !got.LastScheduled.Equal(want.LastScheduled) || !got.LastActual.Equal(want.LastActual) || !got.LastSuccess.Equal(want.LastSuccess) {
		t.Fatalf("got %+v want %+v", got, want)
	}
	path := filepath.Join(s.dir, "foo.timer.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestEngineFiresOnStartupSec(t *testing.T) {
	t.Parallel()
	fired := make(chan string, 4)
	e, fk := testEngine(t, func(name string) { fired <- name })
	e.Arm(Spec{Name: "foo.timer", Unit: "foo.service", OnStartupSec: 5 * time.Second, OnStartupSecSet: true})
	waitQuiet(t, fired)
	fk.Advance(5 * time.Second)
	if name := waitFired(t, fired); name != "foo.timer" {
		t.Fatalf("fired %q", name)
	}
	snap := e.Status("foo.timer")
	if snap.Last.IsZero() {
		t.Fatal("last elapse not recorded")
	}
}

func TestEngineDisarmPreventsFire(t *testing.T) {
	t.Parallel()
	fired := make(chan string, 1)
	e, fk := testEngine(t, func(name string) { fired <- name })
	e.Arm(Spec{Name: "foo.timer", OnStartupSec: 5 * time.Second, OnStartupSecSet: true})
	e.Disarm("foo.timer")
	fk.Advance(5 * time.Second)
	waitQuiet(t, fired)
}

func TestEnginePersistentCatchup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	cal, err := ParseCalendar("daily")
	if err != nil {
		t.Fatal(err)
	}
	fk := NewFake(time.Time{})
	if err := s.Save("backup.timer", Runtime{LastActual: fk.Now().Add(-48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	fired := make(chan string, 1)
	e := NewEngine(fk.Clock(), s, func(name string) { fired <- name })
	t.Cleanup(e.Stop)
	e.Arm(Spec{Name: "backup.timer", OnCalendar: []Calendar{cal}, Persistent: true})
	if name := waitFired(t, fired); name != "backup.timer" {
		t.Fatalf("fired %q", name)
	}
}

func TestEngineOnUnitActiveSecDoesNotRefireSameDue(t *testing.T) {
	t.Parallel()
	fired := make(chan string, 32)
	e, fk := testEngine(t, func(name string) { fired <- name })
	interval := 5 * time.Second
	e.Arm(Spec{
		Name:               "foo.timer",
		Unit:               "foo.service",
		OnUnitActiveSec:    interval,
		OnUnitActiveSecSet: true,
	})
	e.UnitActive("foo.service", fk.Now())
	fk.Advance(interval)
	if name := waitFired(t, fired); name != "foo.timer" {
		t.Fatalf("fired %q", name)
	}
	fk.Advance(2 * interval)
	waitQuiet(t, fired)
	e.UnitActive("foo.service", fk.Now())
	fk.Advance(interval)
	waitFired(t, fired)
}

func TestEngineCalendarJumpAcrossDeadline(t *testing.T) {
	t.Parallel()
	fired := make(chan string, 4)
	e, fk := testEngine(t, func(name string) { fired <- name })
	cal, err := ParseCalendar("*-*-* 15:00:00")
	if err != nil {
		t.Fatal(err)
	}
	// Fake starts at 12:00; next match is 15:00. Jump to 16:00, across the deadline.
	e.Arm(Spec{Name: "cal.timer", OnCalendar: []Calendar{cal}})
	waitNext(t, e, "cal.timer", time.Date(2026, 9, 1, 15, 0, 0, 0, time.UTC))
	waitQuiet(t, fired)
	fk.JumpWall(fk.Now().Add(4 * time.Hour))
	if name := waitFired(t, fired); name != "cal.timer" {
		t.Fatalf("fired %q", name)
	}
}

func TestEngineCalendarJumpBackwardRecalc(t *testing.T) {
	t.Parallel()
	e, fk := testEngine(t, nil)
	cal, err := ParseCalendar("*-*-* 15:00:00")
	if err != nil {
		t.Fatal(err)
	}
	// Start at 16:00 so the next 15:00 is tomorrow.
	fk.JumpWall(time.Date(2026, 9, 1, 16, 0, 0, 0, time.UTC))
	e.Arm(Spec{Name: "cal.timer", OnCalendar: []Calendar{cal}})
	tomorrow := time.Date(2026, 9, 2, 15, 0, 0, 0, time.UTC)
	waitNext(t, e, "cal.timer", tomorrow)
	fk.JumpWall(time.Date(2026, 9, 1, 14, 0, 0, 0, time.UTC))
	today := time.Date(2026, 9, 1, 15, 0, 0, 0, time.UTC)
	waitNext(t, e, "cal.timer", today)
}

func TestEngineOnUnitActiveSecAcrossSuspend(t *testing.T) {
	t.Parallel()
	fired := make(chan string, 4)
	e, fk := testEngine(t, func(name string) { fired <- name })
	e.Arm(Spec{
		Name:               "foo.timer",
		Unit:               "foo.service",
		OnUnitActiveSec:    time.Hour,
		OnUnitActiveSecSet: true,
	})
	e.UnitActive("foo.service", fk.Now())
	waitNext(t, e, "foo.timer", fk.Now().Add(time.Hour))
	waitQuiet(t, fired)
	fk.Suspend(2 * time.Hour)
	if name := waitFired(t, fired); name != "foo.timer" {
		t.Fatalf("fired %q", name)
	}
}

func TestEngineClockChangedFiresCalendarWithoutPoll(t *testing.T) {
	t.Parallel()
	fired := make(chan string, 4)
	fk := NewFake(time.Time{})
	clk := fk.Clock()
	clk.Changed = nil
	e := NewEngine(clk, nil, func(name string) { fired <- name })
	t.Cleanup(e.Stop)
	cal, err := ParseCalendar("*-*-* 15:00:00")
	if err != nil {
		t.Fatal(err)
	}
	e.Arm(Spec{Name: "cal.timer", OnCalendar: []Calendar{cal}})
	today := time.Date(2026, 9, 1, 15, 0, 0, 0, time.UTC)
	waitNext(t, e, "cal.timer", today)
	waitQuiet(t, fired)
	fk.JumpWall(fk.Now().Add(4 * time.Hour))
	waitQuiet(t, fired)
	if got := e.Status("cal.timer").Next; !got.Equal(time.Date(2026, 9, 2, 15, 0, 0, 0, time.UTC)) {
		t.Fatalf("Next stayed stale after wall jump: %v", got)
	}
	e.ClockChanged()
	if name := waitFired(t, fired); name != "cal.timer" {
		t.Fatalf("fired %q", name)
	}
}

func TestEngineClockChangedLeavesOnBootSecHeap(t *testing.T) {
	t.Parallel()
	fired := make(chan string, 4)
	e, fk := testEngine(t, func(name string) { fired <- name })
	e.Arm(Spec{Name: "boot.timer", OnBootSec: 2 * time.Hour, OnBootSecSet: true})
	wantNext := fk.Now().Add(time.Hour) // Fake starts with 1h since boot
	waitNext(t, e, "boot.timer", wantNext)
	gen, when := armedHeap(t, e, "boot.timer")
	waitQuiet(t, fired)
	fk.JumpWall(fk.Now().Add(4 * time.Hour))
	e.ClockChanged()
	waitQuiet(t, fired)
	gen2, when2 := armedHeap(t, e, "boot.timer")
	if gen2 != gen || !when2.Equal(when) {
		t.Fatalf("OnBootSec heap rewritten: gen %d->%d when %v->%v", gen, gen2, when, when2)
	}
	fk.Advance(time.Hour)
	if name := waitFired(t, fired); name != "boot.timer" {
		t.Fatalf("fired %q", name)
	}
}

func TestEngineStopWaitsForFire(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	fk := NewFake(time.Time{})
	e := NewEngine(fk.Clock(), nil, func(string) {
		close(started)
		<-release
	})
	e.Arm(Spec{Name: "foo.timer", OnStartupSec: time.Second, OnStartupSecSet: true})
	fk.Advance(time.Second)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("fire did not start")
	}
	done := make(chan struct{})
	go func() {
		e.Stop()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("Stop returned before the fire goroutine finished")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return after the fire goroutine finished")
	}
}

func armedHeap(t *testing.T, e *Engine, name string) (gen uint64, when time.Time) {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	a := e.armed[name]
	if a == nil {
		t.Fatalf("not armed: %s", name)
	}
	return a.gen, a.next
}
