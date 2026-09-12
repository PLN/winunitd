package timers

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestPersistentMonthEndRecoveryAcrossEngineRestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cal, err := ParseCalendar("*-*-31 00:00:00")
	if err != nil {
		t.Fatal(err)
	}
	spec := Spec{Name: "month.timer", Unit: "month.service", OnCalendar: []Calendar{cal}, Persistent: true}
	var count atomic.Int32
	fired := make(chan string, 8)
	open := func(now time.Time) (*Engine, *Fake, *Store) {
		t.Helper()
		s, err := OpenStore(dir) // Store owns no open handles; reopening rereads disk.
		if err != nil {
			t.Fatal(err)
		}
		fk := NewFake(now)
		var e *Engine
		e = NewEngine(fk.Clock(), s, func(event Fire) {
			e.RecordResult(event, true)
			count.Add(1)
			select {
			case fired <- event.Name:
			default: // A broken repeat loop must not block Stop during test cleanup.
			}
		})
		t.Cleanup(e.Stop)
		e.Arm(spec)
		return e, fk, s
	}
	date := func(month time.Month, day int) time.Time {
		return time.Date(2026, month, day, 0, 0, 0, 0, time.UTC)
	}
	e, fk, s := open(date(time.July, 30))
	waitNext(t, e, spec.Name, date(time.July, 31))
	fk.Advance(24 * time.Hour)
	e.ClockChanged()
	waitFired(t, fired)
	e.Stop() // Join callbacks before inspecting persisted state or simulating reboot.
	if got := s.Load(spec.Name).LastActual; !got.Equal(date(time.July, 31)) {
		t.Fatalf("persisted last actual = %v", got)
	}

	// The manager was down for August 31. September has no 31st.
	e, _, s = open(date(time.September, 1))
	waitFired(t, fired)
	waitNext(t, e, spec.Name, date(time.October, 31))
	e.Stop()
	if got := count.Load(); got != 2 {
		t.Fatalf("initial activation plus one catch-up = %d, want 2", got)
	}
	if got := s.Load(spec.Name).LastActual; !got.Equal(date(time.September, 1)) {
		t.Fatalf("persisted catch-up = %v", got)
	}

	// Another reboot must retain the catch-up and await the next valid date.
	e, fk, _ = open(date(time.September, 1))
	waitNext(t, e, spec.Name, date(time.October, 31))
	fk.Advance(date(time.October, 31).Sub(fk.Now()))
	e.ClockChanged()
	waitFired(t, fired)
	waitNext(t, e, spec.Name, date(time.December, 31))
	e.Stop()
	if got := count.Load(); got != 3 {
		t.Fatalf("activation count after next valid date = %d, want 3", got)
	}
}
