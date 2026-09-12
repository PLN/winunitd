package manager

import (
	"context"
	"time"
)

type clockBudgetKey struct{}
type clockBudget struct{ started, duration time.Duration }

var stopBudgetEpoch = time.Now()

func (m *Manager) clockElapsed() time.Duration {
	if elapsed := m.clock().SinceStart; elapsed != nil {
		return elapsed()
	}
	return time.Since(stopBudgetEpoch)
}

// Carry the accepted monotonic budget through contexts whose cancellation is
// driven by the manager clock rather than context.WithDeadline's wall timer.
func (m *Manager) withClockBudget(ctx context.Context, duration time.Duration) context.Context {
	if duration <= 0 {
		return ctx
	}
	return context.WithValue(ctx, clockBudgetKey{}, clockBudget{started: m.clockElapsed(), duration: m.remainingStopBudget(ctx, duration)})
}

func (m *Manager) remainingStopBudget(ctx context.Context, limit time.Duration) time.Duration {
	if ctx.Err() != nil {
		return 0
	}
	if budget, ok := ctx.Value(clockBudgetKey{}).(clockBudget); ok {
		elapsed := max(0, m.clockElapsed()-budget.started)
		limit = min(limit, max(0, budget.duration-elapsed))
	}
	if deadline, ok := ctx.Deadline(); ok {
		limit = min(limit, max(0, time.Until(deadline)))
	}
	return max(0, limit)
}

// Keep at least a fifth for forced cleanup, with a one-second floor for normal
// budgets and a half-budget ceiling for short explicitly configured timeouts.
func stopForceReserve(total time.Duration) time.Duration {
	return min(total/2, max(time.Second, total/5))
}
