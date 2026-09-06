package timers

import (
	"testing"
	"time"
)

func TestPoppedDeadlineCannotConsumeReplacement(t *testing.T) {
	clock := NewFake(time.Time{})
	store, err := OpenStore("")
	if err != nil {
		t.Fatal(err)
	}
	// Run the scheduler steps explicitly to control the gap between dequeue
	// and consumption without racing a background scheduler goroutine.
	e := &Engine{clk: clock.Clock(), store: store, armed: make(map[string]*armed), wakeup: make(chan struct{}, 1), running: true}
	spec := Spec{Name: "work.timer", OnStartupSec: time.Second, OnStartupSecSet: true}
	e.Arm(spec)
	clock.Advance(2 * time.Second)
	due, ok := e.popDue(e.clk.now())
	if !ok {
		t.Fatal("original deadline was not due")
	}
	e.Disarm(spec.Name)
	e.Arm(spec)
	e.consume(due, e.clk.now())
	e.mu.Lock()
	fresh := e.armed[spec.Name].rt
	e.mu.Unlock()
	if fresh.FiredStartup || !fresh.LastActual.IsZero() {
		t.Fatal("old deadline consumed replacement timer")
	}
}

func TestOldHeapEntryCannotFireReplacementSchedule(t *testing.T) {
	clock := NewFake(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))
	store, err := OpenStore("")
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{clk: clock.Clock(), store: store, armed: make(map[string]*armed), wakeup: make(chan struct{}, 1), running: true}
	old, err := ParseCalendar("*-*-* 13:00:00")
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := ParseCalendar("*-*-* 15:00:00")
	if err != nil {
		t.Fatal(err)
	}
	e.Arm(Spec{Name: "work.timer", OnCalendar: []Calendar{old}})
	e.Disarm("work.timer")
	e.Arm(Spec{Name: "work.timer", OnCalendar: []Calendar{fresh}})
	clock.Advance(2 * time.Hour)
	if _, ok := e.popDue(e.clk.now()); ok {
		t.Fatal("old heap entry fired replacement schedule early")
	}
	clock.Advance(time.Hour)
	due, ok := e.popDue(e.clk.now())
	if !ok {
		t.Fatal("replacement deadline did not fire")
	}
	e.consume(due, e.clk.now())
	e.mu.Lock()
	actual := e.armed["work.timer"].rt.LastActual
	e.mu.Unlock()
	if !actual.Equal(clock.Now()) {
		t.Fatal("current deadline was not consumed")
	}
}
