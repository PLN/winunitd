package manager

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/timers"
)

// DefaultOperationTimeout is the minimum automatically derived operation budget.
const DefaultOperationTimeout = 5 * time.Minute

// Accepted work owns its context and admission slot until its workers return.
// Cancellation ends response waits, but does not abandon native work or history.
type operationTask struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
	timer  timers.Timer
	flight *startFlight
	plans  map[string]*plannedStart
}

// The default allows the serialized sum of configured member budgets, with a
// five-minute floor. An embedding application may choose an explicit override.
// Overflow saturates rather than turning a long valid budget into immediate expiry.
func (m *Manager) operationTimeoutLocked(start, stop *core.Transaction) time.Duration {
	if m.cfg.OperationTimeout > 0 {
		return m.cfg.OperationTimeout
	}
	budget := defaultStopTimeout
	add := func(d time.Duration) {
		if d > time.Duration(math.MaxInt64)-budget {
			budget = time.Duration(math.MaxInt64)
		} else if d > 0 {
			budget += d
		}
	}
	for _, phase := range []*core.Transaction{start, stop} {
		if phase == nil {
			continue
		}
		for _, name := range phase.Units() {
			rt := m.units[name]
			if rt == nil {
				continue
			}
			u := rt.ownedUnit()
			if phase == start {
				u = rt.unit
				if u != nil && u.Service != nil {
					add(max(defaultSCMStartTimeout, u.Service.TimeoutStartSec))
				}
			}
			add(stopTimeout(u))
			add(stopTimeout(u)) // cleanup and capture finalization
		}
	}
	return max(DefaultOperationTimeout, budget)
}

// Call with m.mu held, after admission and before handing work to a goroutine.
func (m *Manager) beginOperationTaskLocked(flight *startFlight, timeout time.Duration, plans map[string]*plannedStart) *operationTask {
	ctx, cancel := context.WithCancelCause(context.Background())
	task := &operationTask{ctx: ctx, cancel: cancel, timer: m.clock().Timer(timeout), flight: flight, plans: plans}
	flight.operationContext = ctx
	if m.activeOperations == nil {
		m.activeOperations = make(map[string]*operationTask)
	}
	m.activeOperations[flight.id] = task
	m.operations[flight.id].DeadlineAt = m.now().Add(timeout).UTC().Format(time.RFC3339Nano)
	go func() {
		select {
		case <-task.timer.C():
			m.mu.Lock()
			if m.activeOperations[flight.id] == task {
				m.cancelOperationLocked(task, fmt.Errorf("operation deadline: %w", context.DeadlineExceeded))
			}
			m.mu.Unlock()
		case <-flight.done:
		}
	}()
	return task
}

func (m *Manager) cancelOperationsLocked() {
	for _, task := range m.activeOperations {
		// Accepted stops retain their independent cleanup budget.
		if task.plans == nil {
			continue
		}
		m.cancelOperationLocked(task, errors.New("manager is shutting down or closed"))
	}
}

func (m *Manager) finishOperationTask(name string, task *operationTask, result *protocol.UnitResult, err error, releaseLocked func()) {
	m.mu.Lock()
	delete(m.activeOperations, task.flight.id)
	task.timer.Stop()
	err = errors.Join(err, context.Cause(task.ctx))
	releaseLocked()
	m.mu.Unlock()
	result, err = m.finishOperation(task.flight.id, result, err)
	m.finishStartFlight(name, task.flight, result, err)
	task.cancel(context.Canceled)
}

// Complete under the same lock as cancellation. A timer cannot disarm a member
// between a successful completion decision and publication of that decision.
func (m *Manager) publishOperationStart(ctx context.Context, event startCompletion) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ctx.Err() != nil {
		return false
	}
	m.applyStartCompletionLocked(event)
	return true
}

func (m *Manager) executeOperationStart(ctx context.Context, member string, planned *plannedStart) error {
	unlock, err := m.ops.lockContext(ctx, member)
	if err != nil {
		m.applyStartRejection(startCompletion{name: member, plan: planned, err: err})
		return err
	}
	released := false
	release := func() {
		if !released {
			released = true
			unlock()
		}
	}
	defer release()
	err = m.launchUnitConfigOp(ctx, member, false, planned)
	if m.publishOperationStart(ctx, startCompletion{name: member, plan: planned, err: err}) {
		return err
	}
	err = errors.Join(err, context.Cause(ctx))
	m.mu.Lock()
	rt := m.units[member]
	ownsLaunch := planned.launched && planned.launchGen != 0 && rt == planned.record && rt.gen == planned.launchGen
	m.mu.Unlock()
	if ownsLaunch {
		// The expired operation retains the gate while adopting/cleaning late
		// work. A fresh cleanup allowance bounds the wait, not native ownership.
		cleanup, cancel := m.clockTimeout(context.Background(), stopTimeout(planned.unit))
		_, cleanupErr := m.stopUnitAfterLock(cleanup, member, release)
		cancel()
		err = errors.Join(err, cleanupErr)
	}
	m.applyStartCompletion(startCompletion{name: member, plan: planned, err: err})
	return err
}

// operationStarter publishes both adapter completions and never-launched graph
// failures as individual events. Execute's aggregate Run remains diagnostic only.
type operationStarter struct {
	manager     *Manager
	definitions map[string]*plannedStart
}

func (s operationStarter) Start(ctx context.Context, name string) error {
	return s.manager.executeOperationStart(ctx, name, s.definitions[name])
}

func (s operationStarter) StartRejected(name string, err error) {
	s.manager.applyStartRejection(startCompletion{name: name, plan: s.definitions[name], err: err})
}
