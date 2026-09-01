package manager

import (
	"context"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/unit"
)

// Shutdown stops units in reverse After=/Before= order, then the caller
// closes the daemon Job Object (DESIGN.md §42). Builtin shutdown.target
// is the stop-transaction root when it is loaded; active units are extra
// roots so winctl-started services go down even if they are not After=
// shutdown.target. Timers are disarmed first so they cannot fire during
// stop. Pending Restart= relaunch is cancelled by each unit stop (M6).
func (m *Manager) Shutdown(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if m.engine != nil {
		m.engine.Retain(nil)
		m.engine.Stop()
	}

	m.mu.Lock()
	g := m.graph
	roots := m.shutdownRootsLocked()
	m.mu.Unlock()
	if g == nil || len(roots) == 0 {
		return nil
	}

	run, err := g.Shutdown(ctx, core.StopFunc(m.stopUnitCtx), roots...)
	m.mu.Lock()
	m.applyRunLocked(run)
	m.mu.Unlock()
	return err
}

func (m *Manager) shutdownRootsLocked() []string {
	seen := make(map[string]struct{})
	var roots []string
	add := func(name string) {
		name = core.NormalizeName(name)
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		if _, ok := m.units[name]; !ok {
			return
		}
		seen[name] = struct{}{}
		roots = append(roots, name)
	}
	add(ShutdownTarget)
	for name, rt := range m.units {
		if rt != nil && rt.state != core.Inactive {
			add(name)
		}
		if rt != nil && rt.proc != nil {
			add(name)
		}
	}
	return roots
}

func (m *Manager) stopTransaction(name string) (*protocol.UnitResult, error) {
	m.mu.Lock()
	g := m.graph
	if _, ok := m.units[name]; !ok {
		m.mu.Unlock()
		return nil, protocol.ErrNotFound(name)
	}
	m.mu.Unlock()
	if g == nil {
		return nil, protocol.ErrFailed("no units loaded")
	}

	run, err := g.Stop(context.Background(), core.StopFunc(m.stopUnitCtx), name)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.applyRunLocked(run)
	if err != nil {
		m.setErrLocked(name, err.Error())
		return &protocol.UnitResult{
			Unit:        name,
			ActiveState: m.stateOfLocked(name).String(),
			Error:       err.Error(),
		}, protocol.ErrFailed(err.Error())
	}
	m.clearErrLocked(name)
	return &protocol.UnitResult{Unit: name, ActiveState: m.stateOfLocked(name).String()}, nil
}

func (m *Manager) stopUnitCtx(ctx context.Context, name string) error {
	_ = ctx
	_, err := m.stopUnit(name)
	return err
}

func (m *Manager) stopUnit(name string) (*protocol.UnitResult, error) {
	if norm, nerr := requireUnit(name); nerr == nil {
		name = norm
		// Set stopping before ops.lock so Type=notify waitReady
		// (which holds that lock until READY=1 or TimeoutStartSec)
		// can return. Otherwise Stop waits for waitReady and
		// waitReady waits for stopping — a deadlock on a fake
		// clock that never Advances TimeoutStartSec.
		m.mu.Lock()
		if rt := m.units[name]; rt != nil {
			rt.stopping = true
			rt.cancelRestart()
		}
		m.mu.Unlock()
	}

	unlock := m.ops.lock(name)
	defer unlock()

	m.mu.Lock()
	rt, err := m.lookup(name)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	name = rt.unit.Name
	proc := rt.takeProc()
	rt.stopping = true
	rt.gen++
	stopGen := rt.gen
	rt.cancelRestart()
	nrt := rt.notify
	rt.notify = nil
	wdCancel := rt.watchdog
	rt.watchdog = nil
	timeout := stopTimeout(rt.unit)
	rt.step(core.EventStopRequested)
	rt.err = ""
	kind := rt.unit.Kind
	scmName := scmServiceName(rt.unit)
	taskName := scheduledTaskName(rt.unit)
	m.mu.Unlock()

	if wdCancel != nil {
		wdCancel()
	}
	if nrt != nil {
		nrt.Close()
	}

	if kind == unit.KindTimer && m.engine != nil {
		m.engine.Disarm(name)
	}
	if kind == unit.KindRegistry {
		m.disarmRegistry(name)
	}
	if kind == unit.KindEventLog {
		m.disarmEventLog(name)
	}

	var stopErr error
	if scmName != "" {
		ctx, cancel := m.clockTimeout(context.Background(), timeout)
		stopErr = m.stopSCM(ctx, scmName, timeout)
		cancel()
	} else if taskName != "" {
		ctx, cancel := m.clockTimeout(context.Background(), timeout)
		stopErr = m.stopTask(ctx, taskName, timeout)
		cancel()
	} else if proc != nil {
		_ = m.stopProcess(proc, timeout)
	}
	if m.journal != nil {
		m.journal.Wait(name)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	rt = m.units[name]
	if rt != nil && !rt.sameOp(stopGen, proc) {
		// A later lifecycle op owns this unit; do not stamp Inactive/Failed
		// over its process (issue #24).
		return &protocol.UnitResult{Unit: name, ActiveState: rt.state.String()}, nil
	}
	if stopErr != nil {
		if rt != nil {
			if rt.step(core.EventStartFailed) {
				rt.err = stopErr.Error()
			}
			return &protocol.UnitResult{
				Unit:        name,
				ActiveState: rt.state.String(),
				Error:       stopErr.Error(),
			}, protocol.ErrFailed(stopErr.Error())
		}
		return &protocol.UnitResult{
			Unit:        name,
			ActiveState: core.Failed.String(),
			Error:       stopErr.Error(),
		}, protocol.ErrFailed(stopErr.Error())
	}
	active := core.Inactive.String()
	if rt != nil {
		rt.step(core.EventStopFinished)
		active = rt.state.String()
	}
	return &protocol.UnitResult{Unit: name, ActiveState: active}, nil
}
