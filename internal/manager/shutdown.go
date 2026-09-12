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
	m.sealShutdownLocked()
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

// Caller holds m.mu. This decision performs no blocking native work.
func (m *Manager) sealShutdownLocked() {
	m.closed = true
	m.cancelOperationsLocked()
	m.signalStartCapacityLocked()
	for _, rt := range m.units {
		rt.stopping = true
		// Publish uncertainty before an outer deadline can return while the
		// stop pass is still blocked in a control/resource close.
		if rt.proc != nil || (rt.unit != nil && rt.state != core.Inactive && (scmServiceName(rt.ownedUnit()) != "" || scheduledTaskName(rt.ownedUnit()) != "")) {
			rt.setCleanup(cleanupWorkload, true)
		}
		if rt.startCancel != nil {
			rt.startCancel()
		}
		rt.cancelRestart()
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
		if rt != nil && (rt.proc != nil || rt.operations != 0 || rt.cleanupPending()) {
			add(name)
		}
	}
	return roots
}

func (m *Manager) stopTransaction(ctx context.Context, name string) (*protocol.UnitResult, error) {
	m.mu.Lock()
	if err := ctx.Err(); err != nil {
		m.mu.Unlock()
		return nil, protocol.ErrFailed(err.Error())
	}
	g := m.graph
	if _, ok := m.units[name]; !ok {
		m.mu.Unlock()
		return nil, protocol.ErrNotFound(name)
	}
	if g == nil {
		m.mu.Unlock()
		return nil, protocol.ErrFailed("no units loaded")
	}
	limit := m.cfg.MaxStopTransactions
	if limit == 0 {
		limit = DefaultMaxStopTransactions
	}
	if m.activeStops >= limit {
		m.mu.Unlock()
		return nil, protocol.ErrFailed("stop transaction capacity exhausted")
	}
	plan, err := g.PlanStop(name)
	if err != nil {
		m.mu.Unlock()
		return nil, protocol.ErrFailed(err.Error())
	}
	m.activeStops++
	m.disarmStopScopeLocked(plan)
	var retained []*unitRuntime
	for _, member := range plan.Units() {
		if rt := m.units[member]; rt != nil {
			rt.operations++
			retained = append(retained, rt)
		}
	}
	operationID := m.beginOperationLocked(name, protocol.MethodStop, nil, plan.Units())
	flight := &startFlight{id: operationID, done: make(chan struct{})}
	task := m.beginOperationTaskLocked(flight, m.operationTimeoutLocked(nil, plan), nil)
	m.mu.Unlock()
	go func() {
		result, err := m.executeStopOperation(task.ctx, name, plan)
		m.finishOperationTask(name, task, result, err, func() {
			m.activeStops--
			for _, rt := range retained {
				rt.operations--
			}
		})
	}()
	return flight.wait(ctx)
}

func (m *Manager) executeStopOperation(ctx context.Context, name string, plan *core.Transaction) (*protocol.UnitResult, error) {
	_, err := plan.ExecuteStop(ctx, core.StopFunc(m.stopAcceptedUnitCtx))
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
		m.disarmStartLocked(name)
		m.mu.Unlock()
	}

	return m.stopAcceptedUnit(ctx, name)
}

func (m *Manager) stopAcceptedUnitCtx(ctx context.Context, name string) error {
	_, err := m.stopAcceptedUnit(ctx, name)
	return err
}

// The transaction already disarmed its entire scope at admission. Do not bump
// the epoch again when an ordered member reaches its gate: that would invalidate
// newer requests and the restart's own captured start intent.
func (m *Manager) stopAcceptedUnit(ctx context.Context, name string) (*protocol.UnitResult, error) {
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
	return m.stopUnitAfterLock(ctx, name, release)
}

// Caller owns the unit gate; release is idempotent and runs before journal waits.
func (m *Manager) stopUnitAfterLock(ctx context.Context, name string, release func()) (*protocol.UnitResult, error) {
	effect, err := m.acceptStopCleanup(name)
	if err != nil {
		return nil, err
	}
	defer m.releaseStopCleanup(effect)
	name = effect.owner.name
	proc, stopOwner, wdCancel := effect.process, effect.owner, effect.cancel
	timeout := stopTimeout(effect.unit)
	ctx, cancelBudget := m.clockTimeout(ctx, timeout)
	defer cancelBudget()
	kind := effect.unit.Kind
	scmName, taskName := scmServiceName(effect.unit), scheduledTaskName(effect.unit)

	if wdCancel != nil {
		wdCancel()
	}
	helperErr := m.cooperativeStop(ctx, stopOwner, effect.unit, proc, effect.stopEligible)

	if kind == unit.KindTimer && m.engine != nil {
		m.engine.Disarm(name)
	}
	var stopErr, hubErr error
	if kind == unit.KindRegistry || kind == unit.KindEventLog || kind == unit.KindPath {
		hubErr = m.disarmHubContext(ctx, name, timeout)
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
	// Publish uncertainty before releasing the operation lock: a queued Start
	// must not replace a process whose termination has not been confirmed.
	m.applyStopCleanup(workloadCleanup{owner: stopOwner, process: proc, err: stopErr})
	notifyErr := m.closeNotifyContext(ctx, name, timeout)
	stopErr = errors.Join(stopErr, notifyErr, hubErr, helperErr)
	// Release the per-unit op lock before journal.Wait so a hung
	// capture cannot block later Start/Stop (issue #68).
	release()
	m.waitJournalContext(ctx, name, timeout)
	stopErr = errors.Join(stopErr, ctx.Err())

	return m.applyStopCompletion(stopCompletion{owner: stopOwner, process: proc, err: stopErr})
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
