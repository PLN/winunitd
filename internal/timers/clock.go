package timers

import "time"

// Clock is the scheduler's view of wall time, machine boot, and this
// winunitd instance start (DESIGN.md §17–§18).
//
// OnBootSec is measured from machine boot. OnStartupSec is measured from
// Startup. They differ after an SCM restart of winunitd.
type Clock struct {
	Now       func() time.Time
	SinceBoot func() time.Duration
	Startup   time.Time
}

// DefaultClock uses the wall clock, platform boot time, and "now" as the
// instance start.
func DefaultClock() Clock {
	now := time.Now()
	return Clock{
		Now:       time.Now,
		SinceBoot: platformSinceBoot,
		Startup:   now,
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
