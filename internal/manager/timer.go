package manager

import (
	"context"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/timers"
	"github.com/PLN/winunitd/internal/unit"
)

func (m *Manager) onTimerElapsed(name string) {
	if m == nil {
		return
	}
	if m.engine != nil && !m.engine.Armed(name) {
		return
	}
	m.mu.Lock()
	st := m.stateOfLocked(name)
	if st == core.Deactivating || st == core.Failed {
		m.mu.Unlock()
		return
	}
	ld := m.units[name]
	activated := ""
	if ld != nil && ld.unit != nil && ld.unit.Timer != nil {
		activated = ld.unit.Timer.Unit
	}
	m.mu.Unlock()
	if activated == "" {
		return
	}
	_, err := m.Start(context.Background(), activated)
	if m.engine != nil {
		m.engine.RecordResult(name, err == nil)
	}
}

func (m *Manager) armTimer(u *unit.Unit) {
	if m == nil || m.engine == nil || u == nil || u.Timer == nil {
		return
	}
	m.engine.Arm(timerSpec(u))
}

func timerSpec(u *unit.Unit) timers.Spec {
	spec := timers.Spec{Name: u.Name}
	if u.Timer == nil {
		return spec
	}
	t := u.Timer
	spec.Unit = t.Unit
	spec.OnBootSec = t.OnBootSec
	spec.OnBootSecSet = t.OnBootSecSet
	spec.OnStartupSec = t.OnStartupSec
	spec.OnStartupSecSet = t.OnStartupSecSet
	spec.OnUnitActiveSec = t.OnUnitActiveSec
	spec.OnUnitActiveSecSet = t.OnUnitActiveSecSet
	spec.OnCalendar = t.OnCalendar
	spec.Persistent = t.Persistent
	return spec
}

func (m *Manager) syncTimersLocked() {
	if m.engine == nil {
		return
	}
	keep := make(map[string]bool)
	var toArm []*unit.Unit
	for name, ld := range m.units {
		if ld == nil || ld.unit == nil || ld.unit.Kind != unit.KindTimer {
			continue
		}
		if m.stateOfLocked(name) != core.Active {
			continue
		}
		keep[name] = true
		toArm = append(toArm, ld.unit)
	}
	m.engine.Retain(keep)
	// Arm after Retain, still without calling fire synchronously.
	for _, u := range toArm {
		m.engine.Arm(timerSpec(u))
	}
}

func formatTimerStamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
