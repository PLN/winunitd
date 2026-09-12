package manager

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/timers"
	"github.com/PLN/winunitd/internal/unit"
)

func (m *Manager) onTimerElapsed(event timers.Fire) {
	name := event.Name
	if m == nil {
		return
	}
	if m.engine != nil && !m.engine.Current(name, event.Token) {
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
	if ld != nil && !ld.unavailable && !m.closed && ld.unit != nil && ld.unit.Timer != nil {
		activated = event.Unit
	}
	m.mu.Unlock()
	if activated == "" {
		return
	}
	_, err := m.startFromOrigin(context.Background(), activated, &timerOrigin{name: name, token: event.Token})
	if m.engine != nil {
		if errors.Is(err, errStartCapacity) {
			m.engine.Retry(event)
			return
		}
		m.engine.RecordResult(event, err == nil)
	}
}

func (m *Manager) armTimer(u *unit.Unit, revision string) error {
	if m == nil || m.engine == nil || u == nil || u.Timer == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rt := m.units[u.Name]
	if rt == nil || rt.unavailable || m.closed {
		return fmt.Errorf("timer %q is unavailable or manager is closed", u.Name)
	}
	spec := timerSpec(u)
	spec.ConfigRevision = revision
	if len(spec.OnCalendar) > timers.MaxCalendarExpressions {
		return fmt.Errorf("timer exceeds %d OnCalendar expressions", timers.MaxCalendarExpressions)
	}
	if m.engine.Arm(spec) == 0 {
		return fmt.Errorf("timer arm capacity %d exhausted or scheduler stopped", timers.MaxArmedTimers)
	}
	return nil
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
		if ld == nil || ld.unavailable || ld.unit == nil || ld.unit.Kind != unit.KindTimer {
			continue
		}
		armed := m.engine.Armed(name)
		if m.stateOfLocked(name) != core.Active && !(armed && ld.operations > 0) {
			continue
		}
		keep[name] = true
		// Keep the captured schedule and callback identity until a fresh arm.
		if !armed {
			toArm = append(toArm, ld.unit)
		}
	}
	m.engine.Retain(keep)
	// Arm after Retain, still without calling fire synchronously.
	for _, u := range toArm {
		spec := timerSpec(u)
		spec.ConfigRevision = m.units[u.Name].configRevision
		m.engine.Arm(spec)
	}
}

func formatTimerStamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// overlayTimer fills Next/Last from the engine after m.mu is released
// so Status/ListUnits/ListTimers do not nest e.mu inside m.mu (issue #66).
func (m *Manager) overlayTimer(st *protocol.UnitStatus) {
	if st == nil || st.Kind != string(unit.KindTimer) {
		return
	}
	if m == nil || m.engine == nil {
		return
	}
	snapshot := m.engine.Status(st.Name)
	st.Next, st.Last = formatTimerStamp(snapshot.Next), formatTimerStamp(snapshot.Last)
	st.ArmedConfigRevision = snapshot.ConfigRevision
	st.TimerStorageState, st.TimerStorageError = snapshot.StorageState, snapshot.StorageError
	st.TimerScheduleState = snapshot.ScheduleState
	st.TimerActivation = timerActivationStatus(snapshot.Activation)
}

func timerActivationStatus(a timers.Activation) *protocol.TimerActivationStatus {
	if a.ID == "" {
		return nil
	}
	return &protocol.TimerActivationStatus{ID: a.ID, Unit: a.Unit, Result: a.Result, Scheduled: formatTimerStamp(a.Scheduled), Actual: formatTimerStamp(a.Actual)}
}

// activationOrigin is checked under m.mu at plan and adapter admission.
type activationOrigin interface {
	validLocked(*Manager) bool
	countsStartLimit() bool
}

type timerOrigin struct {
	name  string
	token uint64
}

func (*timerOrigin) countsStartLimit() bool { return true }

func (o *timerOrigin) validLocked(m *Manager) bool {
	rt := m.units[o.name]
	return !m.closed && rt != nil && !rt.stopping && !rt.unavailable && !rt.stopUncertain && m.engine.Current(o.name, o.token)
}
