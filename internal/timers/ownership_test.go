package timers

import (
	"fmt"
	"sync"
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
	waitNext(t, e, "work.timer", clock.Now().Add(time.Hour))
	e.Disarm("work.timer")
	e.Arm(Spec{Name: "work.timer", OnCalendar: []Calendar{fresh}})
	waitNext(t, e, "work.timer", clock.Now().Add(3*time.Hour))
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

func TestOldFireResultCannotUpdateReplacement(t *testing.T) {
	clock := NewFake(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan Fire, 1)
	e := &Engine{clk: clock.Clock(), store: store, armed: make(map[string]*armed), wakeup: make(chan struct{}, 1), running: true, fire: func(event Fire) { events <- event }}
	spec := Spec{Name: "work.timer", Unit: "old.service", OnStartupSec: time.Second, OnStartupSecSet: true}
	oldToken := e.Arm(spec)
	waitStorageReady(t, e, spec.Name)
	clock.Advance(2 * time.Second)
	due, ok := e.popDue(e.clk.now())
	if !ok {
		t.Fatal("deadline was not due")
	}
	e.consume(due, e.clk.now())
	var old Fire
	select {
	case old = <-events:
	case <-time.After(time.Second):
		t.Fatal("callback missing")
	}
	e.fires.Wait()
	if old.Token != oldToken || old.Unit != "old.service" {
		t.Fatal("callback lost captured arm")
	}
	e.Disarm(spec.Name)
	spec.Unit = "new.service"
	freshToken := e.Arm(spec)
	waitStorageReady(t, e, spec.Name)
	if freshToken == oldToken || e.Current(old.Name, old.Token) {
		t.Fatal("old arm remained current")
	}
	e.RecordResult(old, true)
	if !store.Load(spec.Name).LastSuccess.IsZero() {
		t.Fatal("old result updated replacement persistence")
	}
	fresh := Fire{Name: spec.Name, Unit: spec.Unit, Token: freshToken}
	e.RecordResult(fresh, true)
	if !store.Load(spec.Name).LastSuccess.Equal(clock.Now()) {
		t.Fatal("current result was not saved")
	}
}

func TestCallbackCapacityRetainsUndispatchedDeadlines(t *testing.T) {
	clock := NewFake(time.Time{})
	store, err := OpenStore("")
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	e := &Engine{clk: clock.Clock(), store: store, armed: make(map[string]*armed), wakeup: make(chan struct{}, 1), running: true, fire: func(Fire) { <-release }}
	for i := 0; i < maxTimerCallbacks+7; i++ {
		e.Arm(Spec{Name: fmt.Sprintf("work-%02d.timer", i), OnStartupSec: time.Second, OnStartupSecSet: true})
	}
	clock.Advance(2 * time.Second)
	e.fireDue()
	e.mu.Lock()
	active := e.activeFires
	fired := 0
	for _, a := range e.armed {
		if a.rt.FiredStartup {
			fired++
		}
	}
	e.mu.Unlock()
	if active != maxTimerCallbacks || fired != maxTimerCallbacks {
		t.Fatal("callback limit consumed more deadlines than it admitted")
	}
	unblock()
	e.fires.Wait()
	e.fireDue()
	e.fires.Wait()
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, a := range e.armed {
		if !a.rt.FiredStartup {
			t.Fatal("capacity release lost a pending deadline")
		}
	}
	if e.activeFires != 0 {
		t.Fatal("completed callbacks retained capacity")
	}
}

func TestRetryIsMonotonicAndCannotCrossRearm(t *testing.T) {
	clock := NewFake(time.Time{})
	store, err := OpenStore("")
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{clk: clock.Clock(), store: store, armed: make(map[string]*armed), wakeup: make(chan struct{}, 1), running: true}
	spec := Spec{Name: "work.timer", Unit: "old.service", OnStartupSec: time.Second, OnStartupSecSet: true}
	token := e.Arm(spec)
	clock.Advance(2 * time.Second)
	due, ok := e.popDue(e.clk.now())
	if !ok {
		t.Fatal("initial deadline missing")
	}
	e.consume(due, e.clk.now())
	event := Fire{Name: spec.Name, Unit: spec.Unit, Token: token}
	e.fires.Wait()
	e.Retry(event)
	clock.JumpWall(clock.Now().Add(time.Hour))
	if _, ok := e.popDue(e.clk.now()); ok {
		t.Fatal("wall jump bypassed admission retry delay")
	}
	clock.Advance(admissionRetryDelay)
	if _, ok := e.popDue(e.clk.now()); !ok {
		t.Fatal("pending retry was lost")
	}
	e.Disarm(spec.Name)
	spec.Unit = "new.service"
	spec.OnStartupSec = time.Hour
	e.Arm(spec)
	e.Retry(event)
	if _, ok := e.popDue(e.clk.now()); ok {
		t.Fatal("old retry affected the new arm")
	}
}

func TestCapacityRaceDoesNotSkipPoppedCalendarDeadline(t *testing.T) {
	clock := NewFake(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))
	store, err := OpenStore("")
	if err != nil {
		t.Fatal(err)
	}
	cal, err := ParseCalendar("*-*-* 13:00:00")
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{clk: clock.Clock(), store: store, armed: make(map[string]*armed), wakeup: make(chan struct{}, 1), running: true}
	e.Arm(Spec{Name: "work.timer", OnCalendar: []Calendar{cal}})
	waitNext(t, e, "work.timer", clock.Now().Add(time.Hour))
	clock.Advance(2 * time.Hour)
	due, ok := e.popDue(e.clk.now())
	if !ok {
		t.Fatal("calendar deadline missing")
	}
	e.activeFires = maxTimerCallbacks
	e.consume(due, e.clk.now())
	e.activeFires = 0
	retained, ok := e.popDue(e.clk.now())
	if !ok || !retained.scheduled.Equal(due.scheduled) {
		t.Fatal("capacity race skipped an already-popped calendar deadline")
	}
}
