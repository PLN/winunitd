package timers

import (
	"sync"
	"time"
)

// Fake is a test clock. Advance moves wall time and SinceBoot together
// and fires due NewTimer waits. JumpWall / Suspend move wall time only
// and signal Clock.Changed so calendar and OnUnitActiveSec deadlines
// are recomputed (DESIGN.md §18).
type Fake struct {
	mu         sync.Mutex
	now        time.Time
	boot       time.Duration
	originBoot time.Duration
	startup    time.Time
	timers     map[*fakeTimer]struct{}
	changed    chan struct{}
}

// NewFake starts at now. A zero now uses 2026-09-01 12:00 UTC.
func NewFake(now time.Time) *Fake {
	if now.IsZero() {
		now = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	}
	return &Fake{
		now:        now,
		boot:       time.Hour,
		originBoot: time.Hour,
		startup:    now,
		timers:     make(map[*fakeTimer]struct{}),
		changed:    make(chan struct{}, 1),
	}
}

// Clock is a Clock bound to this Fake. Startup is the Fake's start
// instant and does not move with Advance.
func (f *Fake) Clock() Clock {
	if f == nil {
		return Clock{}
	}
	return Clock{
		Now:        f.Now,
		SinceBoot:  f.SinceBoot,
		SinceStart: f.SinceStart,
		Startup:    f.startup,
		NewTimer:   f.newTimer,
		Changed:    f.changed,
	}
}

// Now is the current wall time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// SinceBoot is the monotonic duration since machine boot.
func (f *Fake) SinceBoot() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.boot
}

// SinceStart is monotonic time since this Fake was created. JumpWall
// and Suspend do not move it; Advance does.
func (f *Fake) SinceStart() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.boot - f.originBoot
	if d < 0 {
		return 0
	}
	return d
}

// Advance moves wall time and SinceBoot forward by d and fires every
// NewTimer whose deadline is at or before the new now.
func (f *Fake) Advance(d time.Duration) {
	if f == nil {
		return
	}
	if d < 0 {
		f.JumpWall(f.Now().Add(d))
		return
	}
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.boot += d
	due := f.takeDueLocked()
	f.mu.Unlock()
	f.deliver(due)
}

// JumpWall sets wall time without moving SinceBoot and signals Changed.
// Pending NewTimer waits are not fired (those are monotonic).
func (f *Fake) JumpWall(to time.Time) {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.now = to
	f.mu.Unlock()
	select {
	case f.changed <- struct{}{}:
	default:
	}
}

// Suspend jumps wall time forward by d without advancing SinceBoot
// (sleep / hibernation). Calendar and wall-based deadlines recompute
// via Changed; relative NewTimer waits do not elapse.
func (f *Fake) Suspend(d time.Duration) {
	if f == nil || d == 0 {
		return
	}
	f.JumpWall(f.Now().Add(d))
}

// Waiting reports whether any NewTimer is pending. Tests wait for the
// engine or manager to arm a fake wait before Advance.
func (f *Fake) Waiting() bool {
	if f == nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.timers) > 0
}

// WaitingAt reports whether a pending NewTimer deadline is exactly now+d.
// NextWhen is the earliest deadline, so a shorter wait hides WatchdogSec
// or RestartSec from advanceArmed; tests wait for this interval to arm.
func (f *Fake) WaitingAt(d time.Duration) bool {
	if f == nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	want := f.now.Add(d)
	for tm := range f.timers {
		if tm.stopped || tm.fired {
			continue
		}
		if tm.when.Equal(want) {
			return true
		}
	}
	return false
}

// NextWhen is the earliest pending NewTimer deadline, if any.
func (f *Fake) NextWhen() (time.Time, bool) {
	if f == nil {
		return time.Time{}, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var earliest time.Time
	found := false
	for tm := range f.timers {
		if tm.stopped || tm.fired {
			continue
		}
		if !found || tm.when.Before(earliest) {
			earliest = tm.when
			found = true
		}
	}
	return earliest, found
}

func (f *Fake) newTimer(d time.Duration) Timer {
	f.mu.Lock()
	defer f.mu.Unlock()
	tm := &fakeTimer{
		fake: f,
		ch:   make(chan time.Time, 1),
		when: f.now.Add(d),
	}
	f.timers[tm] = struct{}{}
	if d <= 0 || !tm.when.After(f.now) {
		f.markFiredLocked(tm)
		now := f.now
		// Buffered send; do not hold callers in Reset/NewTimer.
		select {
		case tm.ch <- now:
		default:
		}
	}
	return tm
}

func (f *Fake) takeDueLocked() []*fakeTimer {
	var due []*fakeTimer
	for tm := range f.timers {
		if tm.stopped || tm.fired {
			continue
		}
		if tm.when.After(f.now) {
			continue
		}
		f.markFiredLocked(tm)
		due = append(due, tm)
	}
	return due
}

func (f *Fake) markFiredLocked(tm *fakeTimer) {
	tm.fired = true
	delete(f.timers, tm)
}

func (f *Fake) deliver(due []*fakeTimer) {
	now := f.Now()
	for _, tm := range due {
		select {
		case tm.ch <- now:
		default:
		}
	}
}

type fakeTimer struct {
	fake    *Fake
	ch      chan time.Time
	when    time.Time
	stopped bool
	fired   bool
}

func (tm *fakeTimer) C() <-chan time.Time { return tm.ch }

func (tm *fakeTimer) Stop() bool {
	tm.fake.mu.Lock()
	defer tm.fake.mu.Unlock()
	if tm.stopped {
		return false
	}
	tm.stopped = true
	delete(tm.fake.timers, tm)
	if tm.fired {
		return false
	}
	tm.fired = true
	return true
}

func (tm *fakeTimer) Reset(d time.Duration) bool {
	tm.fake.mu.Lock()
	defer tm.fake.mu.Unlock()
	active := !tm.stopped && !tm.fired
	select {
	case <-tm.ch:
	default:
	}
	tm.stopped = false
	tm.fired = false
	tm.when = tm.fake.now.Add(d)
	tm.fake.timers[tm] = struct{}{}
	if d <= 0 || !tm.when.After(tm.fake.now) {
		tm.fake.markFiredLocked(tm)
		now := tm.fake.now
		select {
		case tm.ch <- now:
		default:
		}
	}
	return active
}
