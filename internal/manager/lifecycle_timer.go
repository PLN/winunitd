package manager

import (
	"fmt"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/timers"
	"github.com/PLN/winunitd/internal/unit"
)

type timerArm struct {
	token    uint64
	revision string
}

// Timer admission changes only in these serialized decisions. Engine arm/disarm
// calls update bounded in-memory scheduling state and dispatch no synchronous I/O.
func (m *Manager) acceptTimerArmLocked(u *unit.Unit, revision string) error {
	rt := m.units[u.Name]
	if rt == nil || rt.unavailable || m.closed {
		return fmt.Errorf("timer %q is unavailable or manager is closed", u.Name)
	}
	spec := timerSpec(u)
	spec.ConfigRevision = revision
	if len(spec.OnCalendar) > timers.MaxCalendarExpressions {
		return fmt.Errorf("timer exceeds %d OnCalendar expressions", timers.MaxCalendarExpressions)
	}
	token := m.engine.Arm(spec)
	if token == 0 {
		return fmt.Errorf("timer arm capacity %d exhausted or scheduler stopped", timers.MaxArmedTimers)
	}
	rt.timer = &timerArm{token: token, revision: revision}
	return nil
}

func (m *Manager) disarmTimer(owner runtimeIdentity) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !owner.currentLocked(m) {
		return
	}
	if m.engine != nil {
		m.engine.Disarm(owner.name)
	}
	owner.record.timer = nil
}

func (m *Manager) disarmAllTimersLocked() {
	if m.engine != nil {
		m.engine.Retain(nil)
	}
	for _, rt := range m.units {
		if rt != nil {
			rt.timer = nil
		}
	}
}

// Reload retains the exact captured arm of active/in-flight timers. Removed or
// unavailable definitions lose admission immediately; re-arm failures are visible.
func (m *Manager) syncTimersLocked() {
	if m.engine == nil {
		return
	}
	keep := make(map[string]bool)
	var toArm []*unit.Unit
	for name, rt := range m.units {
		if rt == nil || rt.unavailable || rt.unit == nil || rt.unit.Kind != unit.KindTimer {
			continue
		}
		armed := rt.timer != nil
		if rt.state != core.Active && !(armed && rt.operations > 0) {
			continue
		}
		keep[name] = true
		if !armed {
			toArm = append(toArm, rt.unit)
		}
	}
	m.engine.Retain(keep)
	for name, rt := range m.units {
		if rt != nil && !keep[name] {
			rt.timer = nil
		}
	}
	for _, u := range toArm {
		rt := m.units[u.Name]
		if err := m.acceptTimerArmLocked(u, rt.configRevision); err != nil {
			rt.publishStartOutcome(core.Failed)
			rt.err = err.Error()
		}
	}
}
