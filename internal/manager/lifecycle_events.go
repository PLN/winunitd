package manager

import (
	"errors"

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
	m.applyRunLocked(&core.Run{States: map[string]core.State{event.name: state}, Errors: map[string]error{event.name: event.err}})
	if event.err == nil {
		m.clearErrLocked(event.name)
	}
	m.reapFailedLocked()
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
