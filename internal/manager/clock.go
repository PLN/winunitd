package manager

import (
	"context"
	"time"

	"github.com/PLN/winunitd/internal/timers"
)

func (m *Manager) clock() timers.Clock {
	if m == nil {
		return timers.DefaultClock()
	}
	return m.clk
}

func (m *Manager) now() time.Time {
	clk := m.clock()
	if clk.Now != nil {
		return clk.Now()
	}
	return time.Now()
}

// ClockChanged tells the timer engine that wall time jumped (DST, manual
// clock, resume). Calendar deadlines recompute; OnBootSec heap entries
// are left alone (DESIGN.md §18).
func (m *Manager) ClockChanged() {
	if m == nil || m.engine == nil {
		return
	}
	m.engine.ClockChanged()
}

// clockTimeout cancels the child context when d elapses on the manager
// clock (TimeoutStartSec / TimeoutStopSec). Advance on a fake clock fires
// it; the real clock uses time.Timer.
func (m *Manager) clockTimeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(m.withClockBudget(parent, d))
	if d <= 0 {
		return ctx, cancel
	}
	t := m.clock().Timer(d)
	go func() {
		select {
		case <-t.C():
			cancel()
		case <-ctx.Done():
		}
		t.Stop()
	}()
	return ctx, func() {
		t.Stop()
		cancel()
	}
}
