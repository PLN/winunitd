package manager

import (
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/journal"
	"github.com/PLN/winunitd/internal/protocol"
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

func (m *Manager) restartBudgetLocked(rt *unitRuntime) *protocol.RestartBudget {
	if rt == nil {
		return nil
	}
	u := rt.ownedUnit()
	if u == nil {
		return nil
	}
	interval, burst := startLimitOf(u)
	budget := &protocol.RestartBudget{
		IntervalSec:    interval.Seconds(),
		Burst:          burst,
		StartsInWindow: core.StartsInWindow(rt.startTimes, m.now(), interval),
	}
	if u.Service != nil {
		if u.Service.Restart != "" {
			budget.Policy = string(u.Service.Restart)
		}
		if u.Service.RestartMaxDelaySec > 0 {
			budget.MaxDelaySec = u.Service.RestartMaxDelaySec.Seconds()
		}
	}
	if !core.StartLimitDisabled(interval, burst) {
		remaining := burst - budget.StartsInWindow
		if remaining < 0 {
			remaining = 0
		}
		budget.Remaining = &remaining
	}
	return budget
}

func (m *Manager) noteStartLimitLocked(rt *unitRuntime) {
	if m == nil || m.daemonLog == nil || rt == nil {
		return
	}
	interval, burst := startLimitOf(rt.ownedUnit())
	ev := journal.DaemonEvent{
		Code:            journal.DaemonEventStartLimit,
		InvocationID:    rt.invocation,
		OperationID:     rt.lastOperationID,
		ConfigRevision:  rt.configRevision,
		ActiveState:     rt.state.String(),
		Health:          rt.health,
		Reason:          core.ReasonStartLimit,
		RestartAttempt:  rt.restartAttempt,
		HasStartLimit:   true,
		StartLimitBurst: burst,
	}
	if rt.unit != nil {
		ev.Unit = rt.unit.Name
	}
	if rt.unavailable {
		ev.LoadState = "unavailable"
	} else if rt.unit != nil {
		ev.LoadState = "loaded"
	}
	if !core.StartLimitDisabled(interval, burst) {
		remaining := burst - core.StartsInWindow(rt.startTimes, m.now(), interval)
		if remaining < 0 {
			remaining = 0
		}
		ev.StartLimitRemaining = remaining
		ev.HasRemaining = true
	}
	m.daemonLog.Record(ev)
}
