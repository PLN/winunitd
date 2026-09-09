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
