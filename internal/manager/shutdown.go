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

	run, err := g.Stop(ctx, core.StopFunc(m.stopUnitCtx), roots...)
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
	for name := range m.units {
		if m.stateOfLocked(name) != core.Inactive {
			add(name)
		}
	}
	for name, proc := range m.procs {
		if proc != nil {
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
		m.errors[name] = err.Error()
		return &protocol.UnitResult{
			Unit:        name,
			ActiveState: m.stateOfLocked(name).String(),
			Error:       err.Error(),
		}, protocol.ErrFailed(err.Error())
	}
	delete(m.errors, name)
	return &protocol.UnitResult{Unit: name, ActiveState: m.stateOfLocked(name).String()}, nil
}

func (m *Manager) stopUnitCtx(ctx context.Context, name string) error {
	_ = ctx
	_, err := m.stopUnit(name)
	return err
}

func (m *Manager) stopUnit(name string) (*protocol.UnitResult, error) {
	m.mu.Lock()
	ld, err := m.lookup(name)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	name = ld.unit.Name
	proc := m.procs[name]
	delete(m.procs, name)
	m.stopping[name] = true
	m.gens[name]++
	m.cancelRestartLocked(name)
	rt := m.notifies[name]
	delete(m.notifies, name)
	wdCancel := m.watchdogs[name]
	delete(m.watchdogs, name)
	timeout := stopTimeout(ld.unit)
	st, sub := core.Step(m.stateOfLocked(name), m.subOfLocked(name), core.EventStopRequested)
	m.states[name] = st
	m.subs[name] = sub
	delete(m.errors, name)
	kind := ld.unit.Kind
	scmName := scmServiceName(ld.unit)
	m.mu.Unlock()

	if wdCancel != nil {
		wdCancel()
	}
	if rt != nil {
		rt.Close()
	}

	if kind == unit.KindTimer && m.engine != nil {
		m.engine.Disarm(name)
	}

	var stopErr error
	if scmName != "" {
		stopErr = m.stopSCM(context.Background(), scmName, timeout)
	} else if proc != nil {
		_ = proc.Stop(timeout)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if stopErr != nil {
		st, sub = core.Step(m.stateOfLocked(name), m.subOfLocked(name), core.EventStartFailed)
		m.states[name] = st
		m.subs[name] = sub
		m.errors[name] = stopErr.Error()
		return &protocol.UnitResult{
			Unit:        name,
			ActiveState: core.Failed.String(),
			Error:       stopErr.Error(),
		}, protocol.ErrFailed(stopErr.Error())
	}
	st, sub = core.Step(m.stateOfLocked(name), m.subOfLocked(name), core.EventStopFinished)
	m.states[name] = st
	m.subs[name] = sub
	return &protocol.UnitResult{Unit: name, ActiveState: core.Inactive.String()}, nil
}
