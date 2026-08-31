package timers

import "time"

// Spec is one timer unit's schedule (DESIGN.md §17, §74).
type Spec struct {
	Name string
	Unit string // activated unit; empty means same basename .service

	OnBootSec       time.Duration
	OnStartupSec    time.Duration
	OnUnitActiveSec time.Duration
	OnCalendar      []Calendar
	Persistent      bool

	OnBootSecSet       bool
	OnStartupSecSet    bool
	OnUnitActiveSecSet bool
}

// Runtime is per-activation and persistent timer state (DESIGN.md §17).
type Runtime struct {
	LastScheduled  time.Time
	LastActual     time.Time
	LastSuccess    time.Time
	LastUnitActive time.Time
	FiredBoot      bool
	FiredStartup   bool
}

// NextDeadline is the earliest due time for spec given rt and clk.
// ok is false when nothing is scheduled (for example only OnUnitActiveSec
// and the activated unit has never been active).
func NextDeadline(spec Spec, rt Runtime, clk Clock) (time.Time, bool) {
	now := clk.now()
	var earliest time.Time
	found := false
	consider := func(t time.Time) {
		if t.IsZero() {
			return
		}
		if t.Before(now) {
			t = now
		}
		if !found || t.Before(earliest) {
			earliest = t
			found = true
		}
	}

	if spec.OnBootSecSet && !rt.FiredBoot {
		consider(bootDue(spec, clk, now))
	}
	if spec.OnStartupSecSet && !rt.FiredStartup {
		consider(startupDue(spec, clk, now))
	}
	if spec.OnUnitActiveSecSet && !rt.LastUnitActive.IsZero() {
		consider(rt.LastUnitActive.Add(spec.OnUnitActiveSec))
	}
	for _, cal := range spec.OnCalendar {
		if spec.Persistent {
			if catchup, ok := persistentCatchup(cal, rt, now); ok {
				consider(catchup)
				continue
			}
		}
		consider(cal.Next(now))
	}
	return earliest, found
}

func bootDue(spec Spec, clk Clock, now time.Time) time.Time {
	elapsed := clk.sinceBoot()
	if elapsed >= spec.OnBootSec {
		return now
	}
	return now.Add(spec.OnBootSec - elapsed)
}

func startupDue(spec Spec, clk Clock, now time.Time) time.Time {
	due := clk.startup().Add(spec.OnStartupSec)
	if due.Before(now) {
		return now
	}
	return due
}

func persistentCatchup(cal Calendar, rt Runtime, now time.Time) (time.Time, bool) {
	if rt.LastActual.IsZero() {
		return time.Time{}, false
	}
	prev := cal.Previous(now)
	if prev.IsZero() || !prev.After(rt.LastActual) {
		return time.Time{}, false
	}
	return now, true
}

// MarkFired records that a deadline was consumed so one-shot relative
// triggers (OnBootSec / OnStartupSec) do not repeat for this activation.
func MarkFired(spec Spec, rt *Runtime, clk Clock, scheduled, actual time.Time) {
	if rt == nil {
		return
	}
	rt.LastScheduled = scheduled
	rt.LastActual = actual
	if spec.OnBootSecSet && !rt.FiredBoot {
		due := bootDue(spec, clk, actual)
		if !due.After(actual) {
			rt.FiredBoot = true
		}
	}
	if spec.OnStartupSecSet && !rt.FiredStartup {
		due := startupDue(spec, clk, actual)
		if !due.After(actual) {
			rt.FiredStartup = true
		}
	}
}
