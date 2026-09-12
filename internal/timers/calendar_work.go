package timers

import "time"

// One reserved worker visits pending arms in rotating name order. Repeated
// requests coalesce in each arm; old generations cannot publish a deadline.
func (e *Engine) planCalendars() {
	defer e.scheduleWork.Done()
	for {
		e.mu.Lock()
		var next, first *armed
		if e.running {
			for name, a := range e.armed {
				if !a.planning {
					continue
				}
				if first == nil || name < first.spec.Name {
					first = a
				}
				if name > e.scheduleCursor && (next == nil || name < next.spec.Name) {
					next = a
				}
			}
		}
		if next == nil {
			next = first
		}
		if next == nil {
			e.scheduling = false
			e.mu.Unlock()
			return
		}
		name, gen, clockGen := next.spec.Name, next.gen, e.clockGen
		e.scheduleCursor = name
		spec, rt, clock := next.spec, next.rt, e.clk
		e.mu.Unlock()
		// All calendar searches use one captured observation, even if a clock
		// notification arrives during a long search. Its generation invalidates
		// this result; normal passage of time can leave an immediately due result.
		now, boot, start := clock.now(), clock.sinceBoot(), clock.sinceStart()
		frozen := Clock{Now: func() time.Time { return now }, SinceBoot: func() time.Duration { return boot }, SinceStart: func() time.Duration { return start }, Startup: clock.Startup}
		if e.onNextDeadline != nil {
			e.onNextDeadline()
		}
		when, ok := NextDeadline(spec, rt, frozen)
		e.mu.Lock()
		if e.running && e.armed[name] == next && next.gen == gen && e.clockGen == clockGen && next.planning {
			next.planning = false
			next.next, next.ok, next.schedGen = when, ok, clockGen
			if ok {
				e.installDeadlineLocked(next, when, gen)
			}
			e.kickLocked()
		}
		e.mu.Unlock()
	}
}
