package manager

import (
	"context"
	"errors"
	"fmt"
	"time"

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
	if event.err == nil && planned.record.ownedUnit() != nil {
		svc := planned.record.ownedUnit().Service
		if svc != nil && svc.Type == unit.TypeOneshot && !svc.RemainAfterExit {
			state = core.Inactive
		}
	}
	if errors.Is(event.err, core.ErrSkipped) {
		state = core.Inactive
	} else if event.err != nil {
		state = core.Failed
	}
	m.applyStartOutcomeLocked(event.name, state, event.err)
	if event.err == nil {
		m.clearErrLocked(event.name)
		if u := planned.record.ownedUnit(); scmServiceName(u) != "" || scheduledTaskName(u) != "" {
			m.startNativeProbesLocked()
		}
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
	rt.publishStartOutcome(state)
	if (state == core.Inactive || state == core.Failed) && !rt.stopping {
		m.queueBoundStopsLocked(name)
	}
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

// Workload result excludes notification/watch errors; those exact-handle
// decisions retain their own cleanup authority.
type workloadCleanup struct {
	owner   runtimeIdentity
	process runtime.Process
	err     error
}

// Cleanup ownership is published before releasing the unit gate; journal
// draining may finish later, after another operation has acquired that gate.
func (m *Manager) applyStopCleanup(event workloadCleanup) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rt := m.units[event.owner.name]
	if !event.owner.currentLocked(m) || !rt.sameOp(event.owner.gen, event.process) {
		return
	}
	rt.setCleanup(cleanupWorkload, event.err != nil)
	if event.err == nil && rt.proc == event.process {
		rt.proc = nil
		if !rt.cleanupPending() {
			rt.invocationUnit = nil
		}
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
			rt.health, rt.probeFailures = "unknown", 0
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
	// A start without After may have completed while peer cleanup was blocked.
	// Reconcile it before recovery or final exit publication as well.
	if event.service == nil || event.service.Type != unit.TypeOneshot || kind != core.ExitSuccess || !event.service.RemainAfterExit {
		m.queueBoundStopsLocked(event.owner.name)
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
	owner        runtimeIdentity
	unit         *unit.Unit
	process      runtime.Process
	cancel       context.CancelFunc
	stopEligible bool
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
	if !owner.currentLocked(m) || rt.stopping || m.closed || rt.cleanupPending() {
		return nil
	}
	stopEligible := rt.state == core.Active
	if !rt.step(core.EventWatchdogFailed) {
		return nil
	}
	m.queueBoundStopsLocked(owner.name)
	rt.health = "unhealthy"
	rt.err = "watchdog timed out"
	rt.terminated = true
	effect := &watchdogEffect{owner: owner, unit: rt.ownedUnit(), process: rt.proc, cancel: rt.watchdog, stopEligible: stopEligible}
	rt.setCleanup(cleanupWorkload, effect.process != nil)
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
	rt.setCleanup(cleanupWorkload, false)
	rt.terminated = false
	return !rt.cleanupPending()
}

// processExitEffect captures cleanup policy before the worker leaves the
// lifecycle decision. Reload cannot retarget its resources or recovery policy.
type processExitEffect struct {
	owner        runtimeIdentity
	unit         *unit.Unit
	process      runtime.Process
	cancel       context.CancelFunc
	suppressed   bool
	stopEligible bool
}

type processExitCleanup struct {
	effect *processExitEffect
	err    error
}

func (m *Manager) acceptProcessExitCleanup(name string, proc runtime.Process) *processExitEffect {
	m.mu.Lock()
	defer m.mu.Unlock()
	rt := m.units[name]
	if rt == nil || rt.proc != proc {
		return nil
	}
	if !rt.stopping && !rt.terminated {
		m.queueBoundStopsLocked(name)
	}
	if rt.cleanupPending() {
		// A stale watcher must not repeat completed cleanup. An uncertain stop
		// keeps its resource owner even when the main process has exited.
		return nil
	}
	effect := &processExitEffect{
		owner: runtimeIdentity{name: name, record: rt, gen: rt.gen},
		unit:  rt.ownedUnit(), process: proc, cancel: rt.watchdog,
		suppressed:   rt.stopping || rt.terminated,
		stopEligible: rt.state == core.Active,
	}
	rt.setCleanup(cleanupWorkload, true)
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
		if !rt.stopping {
			m.queueBoundStopsLocked(owner.name)
		}
		return false
	}
	rt.proc = nil
	rt.setCleanup(cleanupWorkload, false)
	if rt.cleanupPending() {
		if rt.state != core.Failed {
			rt.step(core.EventStartFailed)
		}
		if !rt.stopping {
			m.queueBoundStopsLocked(owner.name)
		}
		return false
	}
	rt.health, rt.probeFailures = "unknown", 0
	return true
}

// Accept stop precedence for the complete captured scope before dispatching any
// teardown worker. Ordered members cannot recover while an earlier stop waits.
func (m *Manager) disarmStopScopeLocked(plan *core.Transaction) {
	for _, name := range plan.Units() {
		m.disarmStartLocked(name)
	}
}

func (m *Manager) disarmStartLocked(name string) {
	if rt := m.units[name]; rt != nil {
		rt.stopping = true
		rt.stopEpoch++
		m.signalStartCapacityLocked()
		if rt.startCancel != nil {
			rt.startCancel()
		}
		rt.cancelRestart()
	}
}

// stopEffect retains the runtime record and captured invocation definition for
// the entire native cleanup and journal wait. The worker does not choose policy.
type stopEffect struct {
	owner        runtimeIdentity
	unit         *unit.Unit
	process      runtime.Process
	cancel       context.CancelFunc
	stopEligible bool
}

func (m *Manager) acceptStopCleanup(name string) (*stopEffect, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rt, err := m.lookup(name)
	if err != nil {
		return nil, err
	}
	rt.operations++
	rt.stopping = true
	rt.gen++
	effect := &stopEffect{
		owner: runtimeIdentity{name: rt.unit.Name, record: rt, gen: rt.gen},
		unit:  rt.ownedUnit(), process: rt.proc, cancel: rt.watchdog,
		stopEligible: rt.state == core.Active,
	}
	rt.cancelRestart()
	rt.watchdog = nil
	rt.step(core.EventStopRequested)
	if rt.proc != nil || scmServiceName(effect.unit) != "" || scheduledTaskName(effect.unit) != "" {
		rt.setCleanup(cleanupWorkload, true)
	}
	rt.setCleanup(cleanupNotify, rt.notify != nil)
	rt.setCleanup(cleanupWatch, rt.hub != nil)
	rt.err = ""
	return effect, nil
}

func (m *Manager) releaseStopCleanup(effect *stopEffect) {
	m.mu.Lock()
	defer m.mu.Unlock()
	effect.owner.record.operations--
}

type failedProcessEffect struct {
	owner   runtimeIdentity
	process runtime.Process
}

func (m *Manager) reapFailedLocked() {
	for name, rt := range m.units {
		if rt == nil || rt.state != core.Failed || rt.proc == nil || rt.cleanupPending() {
			continue
		}
		effect := failedProcessEffect{owner: runtimeIdentity{name: name, record: rt, gen: rt.gen}, process: rt.proc}
		rt.setCleanup(cleanupWorkload, true)
		go m.reapFailed(effect)
	}
}

func (m *Manager) acceptFailedProcessCleanup(effect failedProcessEffect) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return !m.closed && effect.owner.currentLocked(m) && effect.owner.record.proc == effect.process
}

func (m *Manager) applyFailedProcessCleanup(effect failedProcessEffect, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !effect.owner.currentLocked(m) || effect.owner.record.proc != effect.process {
		return
	}
	rt := effect.owner.record
	if err != nil {
		rt.err = fmt.Sprintf("failed-state cleanup: %v", err)
		return
	}
	rt.proc = nil
	rt.setCleanup(cleanupWorkload, false)
}

// A stop retry may change the operation generation while joining the same
// native close. Handle completion therefore matches the exact retained handle.
type notifyCleanup struct {
	name   string
	notify *notifyRuntime
	err    error
}

func (m *Manager) acceptNotifyCleanup(name string) *notifyRuntime {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rt := m.units[name]; rt != nil && rt.notify != nil {
		rt.setCleanup(cleanupNotify, true)
		return rt.notify
	}
	return nil
}

func (m *Manager) applyNotifyCleanup(event notifyCleanup) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rt := m.units[event.name]; rt != nil && rt.notify == event.notify {
		if event.err == nil {
			rt.notify = nil
			rt.setCleanup(cleanupNotify, false)
		} else {
			rt.setCleanup(cleanupNotify, true)
			rt.err = fmt.Sprintf("notification cleanup: %v", event.err)
		}
	}
}

type hubCleanup struct {
	name string
	hub  *watchRuntime
	err  error
}

func (m *Manager) applyHubCleanup(event hubCleanup) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rt := m.units[event.name]; rt != nil && rt.hub == event.hub {
		if event.err == nil {
			rt.hub = nil
			rt.setCleanup(cleanupWatch, false)
		} else {
			rt.setCleanup(cleanupWatch, true)
			rt.err = fmt.Sprintf("watch cleanup: %v", event.err)
		}
	}
}

// Recovery admission decides eligibility and installs cancellation before the
// worker waits. Delay and launch never run inside this lifecycle decision.
type acceptedRecovery struct {
	context.Context
	delay time.Duration
}

func (m *Manager) acceptRecovery(request recoveryRequest) *acceptedRecovery {
	m.mu.Lock()
	defer m.mu.Unlock()
	owner := request.owner
	rt := m.units[owner.name]
	if m.closed || !owner.currentLocked(m) || rt.stopping || rt.unavailable || rt.cleanupPending() || rt.proc != nil {
		return nil
	}
	if rt.sub == core.SubAutoRestart && rt.restartCancel != nil && rt.restartGeneration == rt.gen {
		return nil // the same invocation already owns its recovery wait
	}
	if m.startLimitHitLocked(rt) {
		m.failStartLimitLocked(rt)
		return nil
	}
	if rt.state == core.Activating && rt.sub == core.SubStart && rt.ownedUnit().Service != nil && rt.ownedUnit().Service.Type == unit.TypeOneshot {
		rt.step(core.EventStartFailed)
	}
	if rt.step(core.EventAutoRestart) {
		rt.err = ""
		m.queueBoundStopsLocked(owner.name)
		if rt.stopping {
			return nil // a dependency cycle included the recovering peer
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	rt.cancelRestart()
	rt.restartCancel = cancel
	rt.restartGeneration = rt.gen
	delay := request.delay
	if u := rt.ownedUnit(); u != nil && u.Service != nil && u.Service.RestartBackoff == "exponential" {
		delay = cappedRestartDelay(restartDelay(u.Service), u.Service.RestartMaxDelaySec, rt.restartAttempt)
	}
	if rt.restartAttempt < 64 {
		rt.restartAttempt++
	}
	rt.restartDelay = delay
	return &acceptedRecovery{Context: ctx, delay: delay}
}

func (m *Manager) acceptWatchdog(name string, gen uint64, cancel context.CancelFunc) (runtimeIdentity, context.CancelFunc, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rt := m.units[name]
	if rt == nil || m.closed || rt.stopping || rt.gen != gen {
		return runtimeIdentity{}, nil, false
	}
	previous := rt.watchdog
	rt.watchdog = cancel
	return runtimeIdentity{name: name, record: rt, gen: gen}, previous, true
}

func (m *Manager) detachWatchdog(name string) context.CancelFunc {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rt := m.units[name]; rt != nil {
		cancel := rt.watchdog
		rt.watchdog = nil
		return cancel
	}
	return nil
}

// Accept native completion before producing a timer activation observation.
// Persistence/scheduler work stays outside the decision; the returned timestamp
// belongs to this accepted observation, not a later worker delivery time.
func (m *Manager) acceptNativeStart(ctx context.Context, owner runtimeIdentity, autoRestart bool) (time.Time, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ctx.Err() != nil || m.closed || !owner.currentLocked(m) || owner.record.stopping {
		return time.Time{}, false
	}
	if autoRestart {
		if !owner.record.step(core.EventStartSucceeded) {
			return time.Time{}, false
		}
		owner.record.err = ""
		m.startNativeProbesLocked()
	}
	return m.now(), true
}

func (m *Manager) retainNotifyDisposal(nrt *notifyRuntime) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rt := m.units[nrt.name]; rt != nil && rt.notify == nil && !m.closed {
		rt.notify = nrt
		return true
	}
	m.closePending = append(m.closePending, unitTeardown{notify: nrt})
	return false
}

func (m *Manager) applyPendingNotifyCleanup(nrt *notifyRuntime, err error) {
	if err != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.closePending[:0]
	for _, td := range m.closePending {
		if td.notify != nrt {
			kept = append(kept, td)
		}
	}
	m.closePending = kept
}
