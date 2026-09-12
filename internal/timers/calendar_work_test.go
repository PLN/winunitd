package timers

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCalendarWorkerCoalescesAndRejectsStaleArms(t *testing.T) {
	e, fk := testEngine(t, nil)
	cal, err := ParseCalendar("*-12-25 00:00:00")
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	e.onNextDeadline = func() {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
	}
	spec := Spec{Name: "work.timer", OnCalendar: []Calendar{cal}}
	e.Arm(spec)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not enter")
	}
	// Requests coalesce while one calculation is blocked. No request spawns
	// another worker or retains the removed arm's deadline.
	for i := 0; i < 1000; i++ {
		e.Disarm(spec.Name)
		e.Arm(spec)
	}
	e.Disarm(spec.Name)
	e.Arm(Spec{Name: spec.Name, OnStartupSec: time.Hour, OnStartupSecSet: true})
	want := fk.Now().Add(time.Hour)
	waitNext(t, e, spec.Name, want)
	if calls.Load() != 1 {
		t.Fatal("concurrent calendar workers")
	}
	unblock()
	e.scheduleWork.Wait()
	if !e.Status(spec.Name).Next.Equal(want) {
		t.Fatal("old calendar result overwrote relative replacement")
	}
}

func TestCalendarWorkerUsesCapturedSpecAndLatestClock(t *testing.T) {
	e, fk := testEngine(t, nil)
	cal, err := ParseCalendar("*-12-25 00:00:00")
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	e.onNextDeadline = func() {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
	}
	spec := Spec{Name: "work.timer", OnCalendar: []Calendar{cal}}
	e.Arm(spec)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not enter")
	}
	// The caller owns its slice; mutation after Arm cannot change the worker.
	spec.OnCalendar[0].Month = 11
	fk.Advance(366 * 24 * time.Hour)
	e.ClockChanged()
	if s := e.Status(spec.Name); s.ScheduleState != "planning" || !s.Next.IsZero() {
		t.Fatalf("stale clock snapshot: %+v", s)
	}
	unblock()
	want := time.Date(2027, 12, 25, 0, 0, 0, 0, time.UTC)
	waitNext(t, e, spec.Name, want)
	if calls.Load() < 2 {
		t.Fatal("clock change did not invalidate captured calculation")
	}
}

func TestCalendarExpressionLimitPreservesExistingArm(t *testing.T) {
	e, fk := testEngine(t, nil)
	spec := Spec{Name: "work.timer", OnStartupSec: time.Hour, OnStartupSecSet: true}
	token := e.Arm(spec)
	spec.OnCalendar = make([]Calendar, MaxCalendarExpressions+1)
	if e.Arm(spec) != 0 {
		t.Fatal("oversized calendar accepted")
	}
	if !e.Current(spec.Name, token) || !e.Status(spec.Name).Next.Equal(fk.Now().Add(time.Hour)) {
		t.Fatal("rejected replacement altered live arm")
	}
}
