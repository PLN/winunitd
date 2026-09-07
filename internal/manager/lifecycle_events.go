package manager

import (
	"context"
	"errors"
	"fmt"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/unit"
)

// startCompletion is delivered while the member's unit gate is still held.
// The transaction keeps its own outcome, but cannot replay lifecycle state.
type startCompletion struct {
	name string
	plan *plannedStart
	err  error
}

func (m *Manager) applyStartCompletion(event startCompletion) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.applyStartCompletionLocked(event)
}

func (m *Manager) applyStartCompletionLocked(event startCompletion) {
	planned := event.plan
	planned.completed = true
	if m.units[event.name] != planned.record || planned.record.stopEpoch != planned.stopEpoch || m.closed || (!planned.launched && planned.origin != nil && !planned.origin.validLocked(m)) {
		return
	}
	state := core.Active
	if errors.Is(event.err, core.ErrSkipped) {
		state = core.Inactive
	} else if event.err != nil {
		state = core.Failed
	}
	m.applyStartOutcomeLocked(event.name, state, event.err)
	if event.err == nil {
		m.clearErrLocked(event.name)
	}
	m.reapFailedLocked()
}

// applyStartOutcomeLocked handles one identity-checked member outcome. Graph
// transaction snapshots are diagnostic results, never lifecycle input.
func (m *Manager) applyStartOutcomeLocked(name string, state core.State, err error) {
	rt := m.units[name]
	if rt == nil || rt.sub == core.SubAutoRestart {
		return
	}
	if state == core.Failed && rt.state == core.Active {
		return // successful members survive a later dependency failure
	}
	if state == core.Active && rt.stopping {
		return
	}
	if state == core.Inactive && !rt.stopping {
		if rt.state == core.Active || rt.state == core.Activating || rt.proc != nil {
			return
		}
	}
	rt.state = state
	if err != nil && rt.state != core.Active {
		rt.err = waitFailMessage(err)
	}
}

// Jobs blocked by dependencies or cancellation never acquire a unit gate.
// Their accepted record/generation must still match at event delivery.
func (m *Manager) applyStartRejection(event startCompletion) {
	m.mu.Lock()
	defer m.mu.Unlock()
	plan := event.plan
	if plan.completed || m.units[event.name] != plan.record || plan.record.gen != plan.gen {
		return
	}
	m.applyStartCompletionLocked(event)
}

type stopCompletion struct {
	owner   runtimeIdentity
	process runtime.Process
	err     error
}

// Cleanup ownership is published before releasing the unit gate; journal
// draining may finish later, after another operation has acquired that gate.
func (m *Manager) applyStopCleanup(event stopCompletion) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rt := m.units[event.owner.name]
	if !event.owner.currentLocked(m) || !rt.sameOp(event.owner.gen, event.process) {
		return
	}
	rt.stopUncertain = event.err != nil
	if event.err == nil && rt.proc == event.process {
		rt.proc = nil
		rt.invocationUnit = nil
	}
}

func (m *Manager) applyStopCompletion(event stopCompletion) (*protocol.UnitResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rt := m.units[event.owner.name]
	if event.owner.currentLocked(m) && rt.sameOp(event.owner.gen, event.process) {
		if event.err != nil {
			if rt.step(core.EventStartFailed) {
				rt.err = event.err.Error()
			}
		} else {
			rt.step(core.EventStopFinished)
		}
	}
	active := core.Inactive.String()
	if rt != nil {
		active = rt.state.String()
	} else if event.err != nil {
		active = core.Failed.String()
	}
	result := &protocol.UnitResult{Unit: event.owner.name, ActiveState: active}
	// A newer operation owns current state, but cannot change this operation's
	// outcome. In particular, a successful retry must not erase an earlier error.
	if event.err != nil {
		result.Error = event.err.Error()
		return result, protocol.ErrFailed(event.err.Error())
	}
	return result, nil
}

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

// watchdogEffect is an accepted cleanup instruction. The worker only performs
// I/O against these captured resources and reports the result back.
type watchdogEffect struct {
	owner   runtimeIdentity
	unit    *unit.Unit
	process runtime.Process
	cancel  context.CancelFunc
}

type watchdogCleanup struct {
	effect *watchdogEffect
	err    error
}

// Called after acquiring the unit gate; a queued timeout revalidates its owner.
func (m *Manager) acceptWatchdogFailure(owner runtimeIdentity) *watchdogEffect {
	m.mu.Lock()
	defer m.mu.Unlock()
	rt := m.units[owner.name]
	if !owner.currentLocked(m) || rt.stopping || m.closed || rt.stopUncertain {
		return nil
	}
	if !rt.step(core.EventWatchdogFailed) {
		return nil
	}
	rt.err = "watchdog timed out"
	rt.terminated = true
	effect := &watchdogEffect{owner: owner, unit: rt.ownedUnit(), process: rt.proc, cancel: rt.watchdog}
	rt.stopUncertain = effect.process != nil
	rt.watchdog = nil
	return effect
}

// Cleanup can release only its exact invocation; recovery checks the same owner
// again after the worker releases the unit gate.
func (m *Manager) applyWatchdogCleanup(event watchdogCleanup) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	owner, proc := event.effect.owner, event.effect.process
	rt := m.units[owner.name]
	if !owner.currentLocked(m) || !rt.sameOp(owner.gen, proc) {
		return false
	}
	if event.err != nil {
		rt.err = fmt.Sprintf("watchdog cleanup: %v", event.err)
		return false
	}
	rt.proc = nil
	rt.stopUncertain = false
	rt.terminated = false
	return true
}

// processExitEffect captures cleanup policy before the worker leaves the
// lifecycle decision. Reload cannot retarget its resources or recovery policy.
type processExitEffect struct {
	owner      runtimeIdentity
	unit       *unit.Unit
	process    runtime.Process
	cancel     context.CancelFunc
	suppressed bool
}

type processExitCleanup struct {
	effect *processExitEffect
	err    error
}

func (m *Manager) acceptProcessExitCleanup(name string, proc runtime.Process) *processExitEffect {
	m.mu.Lock()
	defer m.mu.Unlock()
	rt := m.units[name]
	if rt == nil || rt.proc != proc || rt.stopUncertain {
		// A stale watcher must not repeat completed cleanup. An uncertain stop
		// keeps its resource owner even when the main process has exited.
		return nil
	}
	effect := &processExitEffect{
		owner: runtimeIdentity{name: name, record: rt, gen: rt.gen},
		unit:  rt.ownedUnit(), process: proc, cancel: rt.watchdog,
		suppressed: rt.stopping || rt.terminated,
	}
	rt.stopUncertain = true
	rt.terminated = false
	rt.watchdog = nil
	return effect
}

func (m *Manager) applyProcessExitCleanup(event processExitCleanup) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	owner, proc := event.effect.owner, event.effect.process
	rt := m.units[owner.name]
	if !owner.currentLocked(m) || !rt.sameOp(owner.gen, proc) || rt.proc != proc {
		return false
	}
	if event.err != nil {
		if rt.state != core.Failed {
			rt.step(core.EventStartFailed)
		}
		rt.err = fmt.Sprintf("exit cleanup: %v", event.err)
		return false
	}
	rt.proc = nil
	rt.stopUncertain = false
	return true
}
