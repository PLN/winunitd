package timers

import "time"

// Timer is a one-shot wait that Clock can drive. A fake implementation
// fires due timers from Advance (issue #28, DESIGN.md §18).
type Timer interface {
	C() <-chan time.Time
	Stop() bool
	Reset(d time.Duration) bool
}

// Clock is the scheduler's view of wall time, machine boot, and this
// winunitd instance start (DESIGN.md §17–§18).
//
// OnBootSec is measured from machine boot. OnStartupSec is measured from
// this instance (Clock.SinceStart). They differ after an SCM restart of winunitd.
//
// NewTimer, if set, is used instead of time.NewTimer so a fake can
// Advance time. Changed, if set, is signaled on a discontinuous wall
// jump (manual clock, DST, resume) so the engine recalculates calendar
// deadlines instead of sleeping until a stale monotonic wait. The host
// also calls Engine.ClockChanged on SERVICE_CONTROL_TIMECHANGE /
// POWEREVENT (PBT_APMRESUMEAUTOMATIC). A 30s poll remains as fallback.
//
// SinceStart, if set, is monotonic time since this winunitd instance
// started (OnStartupSec). Nil means now.Sub(Startup) (wall).
type Clock struct {
	Now        func() time.Time
	SinceBoot  func() time.Duration
	SinceStart func() time.Duration
	Startup    time.Time
	NewTimer   func(d time.Duration) Timer
	Changed    <-chan struct{}
}

// DefaultClock uses the wall clock, platform boot time, and "now" as the
// instance start.
func DefaultClock() Clock {
	now := time.Now()
	boot0 := platformSinceBoot()
	return Clock{
		Now:       time.Now,
		SinceBoot: platformSinceBoot,
		SinceStart: func() time.Duration {
			d := platformSinceBoot() - boot0
			if d < 0 {
				return 0
			}
			return d
		},
		Startup:  now,
		NewTimer: stdNewTimer,
	}
}

func (c Clock) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c Clock) sinceBoot() time.Duration {
	if c.SinceBoot != nil {
		return c.SinceBoot()
	}
	return platformSinceBoot()
}

func (c Clock) startup() time.Time {
	if !c.Startup.IsZero() {
		return c.Startup
	}
	return c.now()
}

func (c Clock) sinceStart() time.Duration {
	if c.SinceStart != nil {
		d := c.SinceStart()
		if d < 0 {
			return 0
		}
		return d
	}
	d := c.now().Sub(c.startup())
	if d < 0 {
		return 0
	}
	return d
}

// Timer returns a one-shot wait of d. Tests drive a fake with Advance.
func (c Clock) Timer(d time.Duration) Timer {
	if c.NewTimer != nil {
		return c.NewTimer(d)
	}
	return stdNewTimer(d)
}

func (c Clock) changed() <-chan struct{} {
	return c.Changed
}

type stdTimer struct {
	t *time.Timer
}

func stdNewTimer(d time.Duration) Timer {
	return stdTimer{t: time.NewTimer(d)}
}

func (s stdTimer) C() <-chan time.Time { return s.t.C }

func (s stdTimer) Stop() bool { return s.t.Stop() }

func (s stdTimer) Reset(d time.Duration) bool { return s.t.Reset(d) }
