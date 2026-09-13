package manager

import (
	"context"
	"fmt"

	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/unit"
)

// The launch operation owns the shared creation/readiness budget and process.
// Probes run serially outside the decision mutex and cannot publish lifecycle.
func (m *Manager) waitProbeReadiness(ctx context.Context, owner runtimeIdentity, proc runtime.Process, svc *unit.ServiceSpec) error {
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("startup readiness canceled or timed out: %w", err)
		}
		m.mu.Lock()
		current := owner.currentLocked(m) && !m.closed && !owner.record.stopping && owner.record.proc == proc
		m.mu.Unlock()
		if !current {
			return fmt.Errorf("startup readiness superseded")
		}
		if !proc.Alive() {
			return fmt.Errorf("main process exited before %s readiness", svc.ReadinessMode)
		}
		probeCtx, cancel := context.WithTimeout(ctx, svc.ReadinessTimeoutSec)
		err := svc.ProbeReadiness(probeCtx)
		if err == nil {
			err = probeCtx.Err()
		}
		cancel()
		if err == nil && ctx.Err() == nil && proc.Alive() {
			return nil
		}
		if !proc.Alive() {
			return fmt.Errorf("main process exited before %s readiness", svc.ReadinessMode)
		}
		timer := m.clock().Timer(svc.ReadinessIntervalSec)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("startup readiness canceled or timed out: %w", ctx.Err())
		case <-timer.C():
			timer.Stop()
		}
	}
}
