package timers

import (
	"testing"
	"time"
)

func TestTimerHeapRetainsOneDeadlinePerArm(t *testing.T) {
	clock := NewFake(time.Time{})
	e := &Engine{clk: clock.Clock(), store: &Store{}, armed: make(map[string]*armed), wakeup: make(chan struct{}, 1), running: true}
	e.Arm(Spec{Name: "earlier.timer", OnStartupSecSet: true, OnStartupSec: time.Second})
	spec := Spec{Name: "later.timer", Unit: "work.service", OnUnitActiveSecSet: true, OnUnitActiveSec: time.Hour}
	e.Arm(spec)
	check := func(want int) {
		t.Helper()
		if len(e.pq) != want || len(e.pq) > len(e.armed) {
			t.Fatalf("heap has %d deadlines for %d arms", len(e.pq), len(e.armed))
		}
		for i, it := range e.pq {
			if it.index != i || e.armed[it.name].item != it || e.armed[it.name].gen != it.gen {
				t.Fatal("heap index/owner mismatch")
			}
		}
	}
	// Earlier live entries used to prevent stale later entries from being popped.
	for i := 0; i < 10000; i++ {
		e.UnitActive("work.service", clock.Now().Add(time.Duration(i)*time.Second))
		check(2)
	}
	for i := 0; i < 1000; i++ {
		e.Disarm(spec.Name)
		check(1)
		e.Arm(spec)
		check(1)
		e.UnitActive("work.service", clock.Now())
		check(2)
	}
	e.Retain(map[string]bool{"later.timer": true})
	check(1)
	e.Retain(nil)
	check(0)
}

func TestPoppedTimerDeadlineCanBeRestoredWithoutDuplication(t *testing.T) {
	clock := NewFake(time.Time{})
	e := &Engine{clk: clock.Clock(), store: &Store{}, armed: make(map[string]*armed), wakeup: make(chan struct{}, 1), running: true}
	e.Arm(Spec{Name: "work.timer", OnStartupSecSet: true, OnStartupSec: time.Second})
	clock.Advance(2 * time.Second)
	due, ok := e.popDue(clock.Now())
	if !ok {
		t.Fatal("deadline missing")
	}
	if len(e.pq) != 0 || e.armed["work.timer"].item != nil {
		t.Fatal("popped deadline still linked")
	}
	e.activeFires = maxTimerCallbacks
	e.consume(due, clock.Now())
	if len(e.pq) != 1 || e.armed["work.timer"].item != e.pq[0] {
		t.Fatal("capacity race did not restore owned deadline")
	}
	e.Disarm("work.timer")
	if len(e.pq) != 0 {
		t.Fatal("disarm retained restored deadline")
	}
}
