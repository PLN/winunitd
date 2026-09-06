package manager

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/unit"
)

// Shutdown stops units in reverse After=/Before= order, then the caller
// closes the daemon Job Object (DESIGN.md §42). Builtin shutdown.target
// is the stop-transaction root when it is loaded; active units are extra
// roots so winctl-started services go down even if they are not After=
// shutdown.target. Timers are disarmed first so they cannot fire during
// stop. Admission closes before the stop snapshot; accepted launches are
// included even before they publish a process, and new starts are rejected.
func (m *Manager) Shutdown(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// Close admission before taking the root snapshot. A launch may still be
	// inside the OS with no process reference/state yet; its operation keeps it
	// in the stop plan, which waits for adoption and confirmed cleanup.
	m.mu.Lock()
	m.closed = true
	m.signalStartCapacityLocked()
	for _, rt := range m.units {
		rt.stopping = true
		// Publish uncertainty before an outer deadline can return while the
		// stop pass is still blocked in a control/resource close.
		if rt.proc != nil || (rt.unit != nil && rt.state != core.Inactive && (scmServiceName(rt.ownedUnit()) != "" || scheduledTaskName(rt.ownedUnit()) != "")) {
			rt.stopUncertain = true
		}
		if rt.startCancel != nil {
			rt.startCancel()
		}
		rt.cancelRestart()
	}
	m.mu.Unlock()
	for {
		var ownsPass atomic.Bool
		err := m.stops.wait(ctx, m.clock(), stopKey{shutdown: m}, 0, func() error {
			ownsPass.Store(true)
			return m.shutdownPass(ctx)
		})
		// A retry can first join an older pass whose caller expired while
		// native teardown was pending. Once joined, retry under this caller's
		// live context. A failure from our own pass is returned unchanged.
		if ctx.Err() == nil && !ownsPass.Load() && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) {
			continue
		}
		return err
	}
}

// A shutdown pass can be inside an uncancellable listener/scheduler close.
// Callers retain one pass after their deadline and join it on retry.
func (m *Manager) shutdownPass(ctx context.Context) error {
	m.mu.Lock()
	g := m.graph
	roots := m.shutdownRootsLocked()
	m.mu.Unlock()
	if m.engine != nil {
		m.engine.Retain(nil)
		m.engine.Stop()
	}
	if g == nil || len(roots) == 0 {
		return ctx.Err()
	}

	// Each stop publishes its own generation-checked outcome. The graph's
	// historical summary must not overwrite a later lifecycle operation.
	_, err := g.Shutdown(ctx, core.StopFunc(m.stopUnitCtx), roots...)
	return errors.Join(err, ctx.Err())
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
		if rt != nil && (rt.proc != nil || rt.operations != 0 || rt.stopUncertain) {
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

	_, err := g.Stop(context.Background(), core.StopFunc(m.stopUnitCtx), name)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		return &protocol.UnitResult{
			Unit:        name,
			ActiveState: m.stateOfLocked(name).String(),
			Error:       err.Error(),
		}, protocol.ErrFailed(err.Error())
	}
	return &protocol.UnitResult{Unit: name, ActiveState: m.stateOfLocked(name).String()}, nil
}

func (m *Manager) stopUnitCtx(ctx context.Context, name string) error {
	_, err := m.stopUnitWithContext(ctx, name)
	return err
}

func (m *Manager) stopUnit(name string) (*protocol.UnitResult, error) {
	return m.stopUnitWithContext(context.Background(), name)
}

func (m *Manager) stopUnitWithContext(ctx context.Context, name string) (*protocol.UnitResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
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
			rt.stopEpoch++
			m.signalStartCapacityLocked()
			if rt.startCancel != nil {
				rt.startCancel()
			}
			rt.cancelRestart()
		}
		m.mu.Unlock()
	}

	unlock, lockErr := m.ops.lockContext(ctx, name)
	if lockErr != nil {
		return nil, lockErr
	}
	released := false
	release := func() {
		if !released {
			released = true
			unlock()
		}
	}
	defer release()

	m.mu.Lock()
	rt, err := m.lookup(name)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	name = rt.unit.Name
	rt.operations++
	defer func(record *unitRuntime) {
		m.mu.Lock()
		record.operations--
		m.mu.Unlock()
	}(rt)
	proc := rt.proc
	rt.stopping = true
	rt.gen++
	stopGen := rt.gen
	rt.cancelRestart()
	wdCancel := rt.watchdog
	rt.watchdog = nil
	timeout := stopTimeout(rt.ownedUnit())
	rt.step(core.EventStopRequested)
	rt.err = ""
	kind := rt.ownedUnit().Kind
	scmName := scmServiceName(rt.ownedUnit())
	taskName := scheduledTaskName(rt.ownedUnit())
	m.mu.Unlock()

	if wdCancel != nil {
		wdCancel()
	}
	notifyErr := m.closeNotifyContext(ctx, name, timeout)

	if kind == unit.KindTimer && m.engine != nil {
		m.engine.Disarm(name)
	}
	var stopErr error
	if kind == unit.KindRegistry || kind == unit.KindEventLog || kind == unit.KindPath {
		stopErr = m.disarmHubContext(ctx, name, timeout)
	}
	if scmName != "" {
		ctx, cancel := m.clockTimeout(ctx, timeout)
		stopErr = m.stopSCM(ctx, scmName, timeout)
		cancel()
	} else if taskName != "" {
		ctx, cancel := m.clockTimeout(ctx, timeout)
		stopErr = m.stopTask(ctx, taskName, timeout)
		cancel()
	} else if proc != nil {
		stopErr = m.stopProcessContext(ctx, proc, timeout)
		if stopErr == nil && proc.Alive() {
			stopErr = fmt.Errorf("process remains alive after stop")
		}
	}
	stopErr = errors.Join(stopErr, notifyErr)
	// Publish uncertainty before releasing the operation lock: a queued Start
	// must not replace a process whose termination has not been confirmed.
	m.mu.Lock()
	if rt := m.units[name]; rt != nil && rt.sameOp(stopGen, proc) {
		rt.stopUncertain = stopErr != nil
		if stopErr == nil && rt.proc == proc {
			rt.proc = nil
			rt.invocationUnit = nil
		}
	}
	m.mu.Unlock()
	// Release the per-unit op lock before journal.Wait so a hung
	// capture cannot block later Start/Stop (issue #68).
	release()
	m.waitJournalContext(ctx, name, timeout)
	stopErr = errors.Join(stopErr, ctx.Err())

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

func (m *Manager) waitJournal(name string, timeout time.Duration) {
	m.waitJournalContext(context.Background(), name, timeout)
}

func (m *Manager) waitJournalContext(ctx context.Context, name string, timeout time.Duration) {
	if m == nil || m.journal == nil {
		return
	}
	ctx, cancel := m.clockTimeout(ctx, timeout)
	defer cancel()
	if !m.journal.WaitContext(ctx, name) {
		log.Printf("winunitd: %s: journal wait exceeded TimeoutStopSec", name)
	}
}
