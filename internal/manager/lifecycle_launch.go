package manager

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/unit"
)

type launchObservation struct {
	record  *unitRuntime
	process runtime.Process
	alive   bool
}

type launchEffect struct {
	owner        runtimeIdentity
	unit         *unit.Unit
	revision     string
	previous     runtime.Process
	previousUnit *unit.Unit
}

func (m *Manager) acceptLaunch(ctx context.Context, name string, autoRestart bool, planned *plannedStart, recovery *runtimeIdentity, observation launchObservation) (*launchEffect, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if recovery != nil && (ctx.Err() != nil || !recovery.currentLocked(m)) {
		return nil, nil
	}
	if m.closed {
		if autoRestart {
			return nil, nil
		}
		return nil, fmt.Errorf("manager is shutting down or closed")
	}
	rt := m.units[name]
	if rt == nil {
		if autoRestart {
			return nil, nil
		}
		return nil, fmt.Errorf("unit %q is not loaded", name)
	}
	if autoRestart && rt.stopping {
		return nil, nil
	}
	if planned != nil && (planned.record != rt || planned.stopEpoch != rt.stopEpoch || (planned.origin != nil && !planned.origin.validLocked(m))) {
		return nil, fmt.Errorf("unit %q start superseded by stop", name)
	}
	if rt.cleanupPending() {
		return nil, fmt.Errorf("unit %q termination is unconfirmed; retry stop before starting", name)
	}
	// Bump gen only for a real launch. A redundant Start on a live
	// process must not invalidate the running watchdog (issue #23).
	if rt != observation.record || rt.proc != observation.process {
		return nil, fmt.Errorf("launch observation superseded")
	}
	// Different transaction roots can share a oneshot prerequisite. The unit
	// gate waits for its completion; do not turn that wait into a queued rerun.
	if planned != nil && planned.unit.Service != nil && planned.unit.Service.Type == unit.TypeOneshot && planned.revision == rt.invocationRevision && (planned.joiningOneshot || planned.gen != rt.gen) {
		if _, restart := planned.origin.(*restartOrigin); !restart {
			switch rt.state {
			case core.Inactive, core.Active:
				return nil, nil
			case core.Failed:
				return nil, fmt.Errorf("shared oneshot failed: %s", rt.err)
			default:
				return nil, fmt.Errorf("shared oneshot is recovering; retry start after completion")
			}
		}
	}
	if observation.alive {
		return nil, nil
	}
	// A retained successful oneshot is already started even without a process.
	// Reload changes the next invocation, not the completed one's policy.
	if owned := rt.ownedUnit(); rt.state == core.Active && owned != nil && owned.Service != nil && owned.Service.Type == unit.TypeOneshot && owned.Service.RemainAfterExit {
		return nil, nil
	}
	if rt.unavailable {
		return nil, fmt.Errorf("unit %q has no valid configuration; reload a valid unit before starting", name)
	}
	// Native start failures can still leave an external resource running. Do
	// not abandon its identity when an explicit start adopts a reloaded unit.
	owned := rt.ownedUnit()
	definition := rt.unit
	revision := rt.configRevision
	if planned != nil {
		definition = planned.unit
		revision = planned.revision
	}
	if !autoRestart && rt.invocationUnit != nil &&
		(scmServiceName(owned) != "" || scheduledTaskName(owned) != "") &&
		(scmServiceName(owned) != scmServiceName(definition) || scheduledTaskName(owned) != scheduledTaskName(definition)) {
		return nil, fmt.Errorf("unit %q still owns a previous native target; stop it before starting the new definition", name)
	}
	if planned != nil {
		planned.launched = true
	}
	triggered := planned != nil && planned.origin != nil && planned.origin.countsStartLimit()
	if triggered {
		interval, burst := startLimitOf(definition)
		if core.StartLimitHit(rt.startTimes, m.now(), interval, burst) {
			rt.cancelRestart()
			m.failStartLimitLocked(rt)
			return nil, errors.New(core.ReasonStartLimit)
		}
	}
	if !autoRestart {
		rt.stopping = false
		rt.cancelRestart()
		if !triggered {
			rt.startTimes = nil
		}
	} else if m.startLimitHitLocked(rt) {
		m.failStartLimitLocked(rt)
		return nil, nil
	}
	// Every real invocation, including automatic recovery, has a fresh
	// generation. Callbacks from the previous process must become stale.
	rt.operations++
	rt.gen++
	startGen := rt.gen
	if planned != nil {
		planned.launchGen = startGen
	}
	// A start can win the operation lock before the exit watcher. Retain the
	// old invocation until cleanup succeeds; a dead main PID is not sufficient.
	evicted := rt.proc
	if evicted != nil {
		rt.setCleanup(cleanupWorkload, true)
	}
	u := definition
	if autoRestart {
		u = owned
		if rt.invocationUnit != nil {
			revision = rt.invocationRevision
		}
	}
	return &launchEffect{owner: runtimeIdentity{name: name, record: rt, gen: startGen}, unit: u, revision: revision, previous: evicted, previousUnit: owned}, nil
}

func (m *Manager) releaseLaunch(effect *launchEffect) {
	m.mu.Lock()
	defer m.mu.Unlock()
	effect.owner.record.operations--
}

func (m *Manager) applyPreviousCleanup(effect *launchEffect, err error) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	rt := effect.owner.record
	if m.closed || !effect.owner.currentLocked(m) || !rt.sameOp(effect.owner.gen, effect.previous) {
		return false
	}
	if err == nil {
		rt.proc = nil
		rt.setCleanup(cleanupWorkload, false)
	} else {
		rt.err = fmt.Sprintf("previous invocation cleanup: %v", err)
	}
	return true
}

func (m *Manager) recordServiceLaunch(effect *launchEffect, native bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || !effect.owner.currentLocked(m) {
		return
	}
	rt := effect.owner.record
	if effect.unit.Service.Type == unit.TypeOneshot {
		rt.step(core.EventStartRequested)
	}
	if native {
		rt.invocationUnit = effect.unit
		rt.invocationRevision = effect.revision
	}
	m.recordStartLocked(rt)
}

func (m *Manager) recordInvocation(effect *launchEffect, invocation string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if effect.owner.currentLocked(m) {
		rt := effect.owner.record
		rt.invocationUnit = effect.unit
		rt.invocationRevision = effect.revision
		rt.invocation = invocation
	}
}

func (m *Manager) adoptLaunchNotify(effect *launchEffect, notify *notifyRuntime) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || !effect.owner.currentLocked(m) {
		return false
	}
	effect.owner.record.notify = notify
	return true
}

func (m *Manager) retainLaunchFailure(effect *launchEffect, proc runtime.Process, pid int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// The retained record and unit gate exclude removal/replacement. Adopt
	// partial creations even when stop/close has superseded their activation.
	rt := effect.owner.record
	rt.proc = proc
	rt.mainPID = pid
	rt.setCleanup(cleanupWorkload, true)
	rt.err = err.Error()
}

type processAdoption struct {
	effect      *launchEffect
	process     runtime.Process
	pid         int
	autoRestart bool
	alive       bool
}

func (m *Manager) adoptProcess(event processAdoption) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	rt := event.effect.owner.record
	// Retention and the unit gate protect the record. Late creations must be
	// adopted before cleanup so failure remains reachable through Stop.
	rt.proc = event.process
	rt.mainPID = event.pid
	if m.closed || rt.stopping || !event.effect.owner.currentLocked(m) {
		rt.setCleanup(cleanupWorkload, true)
		return false
	}
	rt.terminated = false
	if event.effect.unit.Service.Type == unit.TypeNotify {
		rt.step(core.EventStartRequested)
	} else if event.autoRestart && event.alive && event.effect.unit.Service.Type != unit.TypeOneshot {
		if rt.step(core.EventStartSucceeded) {
			rt.err = ""
		}
	}
	return true
}

func (m *Manager) applyLateLaunchCleanup(effect *launchEffect, proc runtime.Process, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !effect.owner.currentLocked(m) || effect.owner.record.proc != proc {
		return
	}
	rt := effect.owner.record
	if err == nil {
		rt.proc = nil
		rt.setCleanup(cleanupWorkload, false)
	} else {
		rt.step(core.EventStartFailed)
		rt.err = fmt.Sprintf("late launch cleanup: %v", err)
	}
}

func (m *Manager) registerStartWait(effect *launchEffect, cancel context.CancelFunc) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || !effect.owner.currentLocked(m) || effect.owner.record.stopping {
		return false
	}
	effect.owner.record.startCancel = cancel
	return true
}

func (m *Manager) clearStartWait(effect *launchEffect) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if effect.owner.currentLocked(m) {
		effect.owner.record.startCancel = nil
	}
}

func (m *Manager) applyOneshotCleanup(effect *launchEffect, proc runtime.Process, cleanupErr, waitErr error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !effect.owner.currentLocked(m) || effect.owner.record.proc != proc {
		return
	}
	rt := effect.owner.record
	if waitErr != nil {
		rt.terminated = true
	}
	rt.setCleanup(cleanupWorkload, cleanupErr != nil)
	if err := errors.Join(waitErr, cleanupErr); err != nil && !rt.stopping {
		rt.step(core.EventStartFailed)
		rt.err = err.Error()
	}
}

// Successful completion owns cleanup and final state before releasing the unit
// gate. A later invocation cannot race the old process's exit watcher.
func (m *Manager) completeOneshot(ctx context.Context, effect *launchEffect, proc runtime.Process, cleanupErr error) (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rt := effect.owner.record
	if !effect.owner.currentLocked(m) || rt.proc != proc {
		return time.Time{}, fmt.Errorf("oneshot completion superseded")
	}
	if cleanupErr != nil {
		rt.setCleanup(cleanupWorkload, true)
		rt.step(core.EventStartFailed)
		rt.err = fmt.Sprintf("oneshot cleanup: %v", cleanupErr)
		return time.Time{}, cleanupErr
	}
	rt.proc = nil
	rt.setCleanup(cleanupWorkload, false)
	if rt.cleanupPending() {
		rt.step(core.EventStartFailed)
		return time.Time{}, fmt.Errorf("oneshot cleanup remains pending: %s", rt.err)
	}
	if ctx.Err() != nil || m.closed || rt.stopping {
		return time.Time{}, fmt.Errorf("oneshot completion canceled: %w", context.Canceled)
	}
	state := core.Inactive
	if effect.unit.Service.RemainAfterExit {
		state = core.Active
	}
	rt.publishStartOutcome(state)
	rt.err = ""
	return m.now(), nil
}

func (m *Manager) acceptReadinessFailure(effect *launchEffect, proc runtime.Process, err error) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !effect.owner.currentLocked(m) || effect.owner.record.proc != proc {
		return true
	}
	rt := effect.owner.record
	rt.terminated = true
	rt.setCleanup(cleanupWorkload, true)
	if !rt.stopping && rt.step(core.EventStartFailed) {
		rt.err = err.Error()
	}
	return rt.stopping
}

func (m *Manager) applyReadinessCleanup(effect *launchEffect, proc runtime.Process, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !effect.owner.currentLocked(m) || !effect.owner.record.sameOp(effect.owner.gen, proc) {
		return
	}
	rt := effect.owner.record
	if err == nil {
		rt.proc = nil
		rt.setCleanup(cleanupWorkload, false)
		rt.terminated = false
	} else {
		rt.err = fmt.Sprintf("readiness cleanup: %v", err)
	}
}

func (m *Manager) acceptProcessActivation(ctx context.Context, effect *launchEffect, proc runtime.Process) (time.Time, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ctx.Err() != nil || m.closed || !effect.owner.currentLocked(m) || effect.owner.record.stopping || effect.owner.record.proc != proc {
		return time.Time{}, false
	}
	rt := effect.owner.record
	if effect.unit.Service.Type == unit.TypeNotify {
		if !rt.step(core.EventStartSucceeded) {
			return time.Time{}, false
		}
		rt.err = ""
	}
	return m.now(), true
}

func (m *Manager) recordStartLocked(rt *unitRuntime) {
	if rt == nil {
		return
	}
	interval, burst := startLimitOf(rt.ownedUnit())
	rt.startTimes = core.RecordStart(rt.startTimes, m.now(), interval, burst)
}

func (m *Manager) failStartLimitLocked(rt *unitRuntime) {
	if rt == nil {
		return
	}
	switch rt.state {
	case core.Active, core.Activating, core.Inactive:
		_ = rt.step(core.EventMainExited)
	}
	rt.err = core.ReasonStartLimit
}
