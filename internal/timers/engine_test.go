package timers

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
	clk := DefaultClock()
	e := NewEngine(clk, nil, func(name string) { fired <- name })
	t.Cleanup(e.Stop)
	e.Arm(Spec{Name: "foo.timer", Unit: "foo.service", OnStartupSec: 40 * time.Millisecond, OnStartupSecSet: true})

	select {
	case name := <-fired:
		if name != "foo.timer" {
			t.Fatalf("fired %q", name)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnStartupSec did not fire")
	}
	snap := e.Status("foo.timer")
	if snap.Last.IsZero() {
		t.Fatal("last elapse not recorded")
	}
}

func TestEngineDisarmPreventsFire(t *testing.T) {
	t.Parallel()
	fired := make(chan string, 1)
	e := NewEngine(DefaultClock(), nil, func(name string) { fired <- name })
	t.Cleanup(e.Stop)
	e.Arm(Spec{Name: "foo.timer", OnStartupSec: 80 * time.Millisecond, OnStartupSecSet: true})
	e.Disarm("foo.timer")
	select {
	case name := <-fired:
		t.Fatalf("disarmed timer fired %q", name)
	case <-time.After(200 * time.Millisecond):
	}
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
	if err := s.Save("backup.timer", Runtime{LastActual: time.Now().Add(-48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	fired := make(chan string, 1)
	e := NewEngine(DefaultClock(), s, func(name string) { fired <- name })
	t.Cleanup(e.Stop)
	e.Arm(Spec{Name: "backup.timer", OnCalendar: []Calendar{cal}, Persistent: true})
	select {
	case name := <-fired:
		if name != "backup.timer" {
			t.Fatalf("fired %q", name)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Persistent=yes did not catch up")
	}
}
