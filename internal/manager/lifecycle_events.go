package manager

import (
	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/unit"
)

// processExitCompletion reports an observed exit after owned process/job
// cleanup succeeded. It carries the captured policy, never a later reload.
type processExitCompletion struct {
	owner      runtimeIdentity
	service    *unit.ServiceSpec
	waitErr    error
	limitHit   bool
	suppressed bool // an explicit stop/readiness/watchdog path owns the outcome
}

// applyProcessExit is a serialized lifecycle decision. The worker completes
// blocking cleanup before delivery; delayed events must match the exact owner.
func (m *Manager) applyProcessExit(event processExitCompletion) {
	if event.suppressed {
		return
	}
	kind := classifyWait(event.waitErr)
	if event.limitHit {
		kind = core.ExitResourceLimit
	}
	m.mu.Lock()
	rt := m.units[event.owner.name]
	if !event.owner.currentLocked(m) || rt.stopping {
		m.mu.Unlock()
		return
	}
	if event.service != nil && core.ShouldRestart(event.service.Restart, kind) {
		m.mu.Unlock()
		m.beginRestart(recoveryRequest{owner: event.owner, delay: restartDelay(event.service)})
		return
	}
	defer m.mu.Unlock()
	if rt.sub == core.SubWatchdog {
		return
	}
	if event.service != nil && event.service.Type == unit.TypeOneshot && kind == core.ExitSuccess {
		return
	}
	if rt.step(core.EventMainExited) {
		if event.limitHit {
			rt.err = core.ReasonResourceLimit
		} else {
			rt.err = mainExitMessage(event.waitErr)
		}
	}
}
