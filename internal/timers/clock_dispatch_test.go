package timers

import (
	"sync"
	"testing"
	"time"
)

func TestClockChangeRetainsCalendarAtCallbackCapacity(t *testing.T) {
	clock := NewFake(time.Time{})
	clk := clock.Clock()
	clk.Changed = nil
	fired := make(chan string, 2)
	e := NewEngine(clk, nil, func(event Fire) { fired <- event.Name })
	t.Cleanup(e.Stop)
	cal, err := ParseCalendar("*-*-* 15:00:00")
	if err != nil {
		t.Fatal(err)
	}
	e.Arm(Spec{Name: "work.timer", OnCalendar: []Calendar{cal}})
	due := time.Date(2026, 9, 1, 15, 0, 0, 0, time.UTC)
	waitNext(t, e, "work.timer", due)
	e.mu.Lock()
	e.activeFires = maxTimerCallbacks
	e.mu.Unlock()
	clock.JumpWall(clock.Now().Add(4 * time.Hour))
	e.ClockChanged()
	if next := e.Status("work.timer").Next; !next.Equal(due) {
		t.Fatalf("saturated clock change lost due occurrence: %v", next)
	}
	e.mu.Lock()
	e.activeFires = 0
	e.mu.Unlock()
	e.fireDue()
	if name := waitFired(t, fired); name != "work.timer" {
		t.Fatal(name)
	}
	waitNext(t, e, "work.timer", due.Add(24*time.Hour))
	waitQuiet(t, fired)
}

func TestConcurrentClockNotificationsConsumeCalendarOnce(t *testing.T) {
	clock := NewFake(time.Time{})
	fired := make(chan string, 16)
	e := NewEngine(clock.Clock(), nil, func(event Fire) { fired <- event.Name })
	t.Cleanup(e.Stop)
	cal, err := ParseCalendar("*-*-* 15:00:00")
	if err != nil {
		t.Fatal(err)
	}
	e.Arm(Spec{Name: "work.timer", OnCalendar: []Calendar{cal}})
	due := time.Date(2026, 9, 1, 15, 0, 0, 0, time.UTC)
	waitNext(t, e, "work.timer", due)
	clock.JumpWall(clock.Now().Add(4 * time.Hour))
	var changes sync.WaitGroup
	for i := 0; i < 16; i++ {
		changes.Add(1)
		go func() { defer changes.Done(); e.ClockChanged() }()
	}
	changes.Wait()
	if name := waitFired(t, fired); name != "work.timer" {
		t.Fatal(name)
	}
	waitNext(t, e, "work.timer", due.Add(24*time.Hour))
	waitQuiet(t, fired)
}
