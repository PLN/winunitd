package manager

import (
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/unit"
)

func startLimitOf(u *unit.Unit) (time.Duration, int) {
	if u == nil {
		return 0, 0
	}
	return u.StartLimitInterval, u.StartLimitBurst
}

func (m *Manager) startLimitHitLocked(rt *unitRuntime) bool {
	if rt == nil {
		return false
	}
	interval, burst := startLimitOf(rt.ownedUnit())
	return core.StartLimitHit(rt.startTimes, m.now(), interval, burst)
}

func (m *Manager) recordStartLocked(rt *unitRuntime) {
	if rt == nil {
		return
	}
	interval, burst := startLimitOf(rt.ownedUnit())
	rt.startTimes = core.RecordStart(rt.startTimes, m.now(), interval, burst)
}

func (m *Manager) failStartLimitLocked(rt *unitRuntime) {
	if rt == nil {
		return
	}
	switch rt.state {
	case core.Active, core.Activating, core.Inactive:
		_ = rt.step(core.EventMainExited)
	}
	rt.err = core.ReasonStartLimit
}
