package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/journal"
	"github.com/PLN/winunitd/internal/notify"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/unit"
)

const (
	defaultStopTimeout  = 5 * time.Second
	defaultRestartDelay = 100 * time.Millisecond
)

func (m *Manager) launchUnit(ctx context.Context, name string, autoRestart bool) error {
	unlock := m.ops.lock(name)
	defer unlock()
	return m.launchUnitOp(ctx, name, autoRestart)
}

func (m *Manager) launchUnitOp(ctx context.Context, name string, autoRestart bool) error {
	return m.launchUnitConfigOp(ctx, name, autoRestart, nil)
}

type plannedStart struct {
	origin         activationOrigin
	unit           *unit.Unit
	revision       string
	record         *unitRuntime
	stopEpoch      uint64
	launched       bool   // adapter work was admitted before source invalidation
	completed      bool   // completion already published while holding the unit gate
	gen            uint64 // generation at plan acceptance, for members never launched
	launchGen      uint64 // exact generation captured when this worker acquires an invocation
	joiningOneshot bool   // this plan observed an invocation already waiting for exit
}

// A transaction passes its captured definition; recovery uses ownedUnit.
func (m *Manager) launchUnitConfigOp(ctx context.Context, name string, autoRestart bool, planned *plannedStart) error {
	return m.launchUnitOwnedOp(ctx, name, autoRestart, planned, nil)
}

// The caller holds the unit gate while observing its current process. Native
// liveness work must not hold the lifecycle mutex and delay unrelated commands.
func (m *Manager) observeLaunch(name string) launchObservation {
	m.mu.Lock()
	observation := launchObservation{record: m.units[name]}
	if observation.record != nil {
		observation.process = observation.record.proc
	}
	inspect := !m.closed && observation.record != nil && !observation.record.cleanupPending()
	m.mu.Unlock()
	if inspect && observation.process != nil {
		observation.alive = observation.process.Alive()
	}
	return observation
}

func (m *Manager) launchUnitOwnedOp(ctx context.Context, name string, autoRestart bool, planned *plannedStart, recovery *runtimeIdentity) error {
	observation := m.observeLaunch(name)
	effect, err := m.acceptLaunch(ctx, name, autoRestart, planned, recovery, observation)
	if err != nil || effect == nil {
		return err
	}
	defer m.releaseLaunch(effect)
	u, revision, evicted, owned := effect.unit, effect.revision, effect.previous, effect.previousUnit
	if evicted != nil {
		cleanupErr := m.stopProcess(evicted, stopTimeout(owned))
		if cleanupErr == nil && evicted.Alive() {
			cleanupErr = fmt.Errorf("process remains alive after previous invocation cleanup")
		}
		stillOwned := m.applyPreviousCleanup(effect, cleanupErr)
		if cleanupErr != nil {
			return fmt.Errorf("previous invocation cleanup: %w", cleanupErr)
		}
		if !stillOwned {
			return fmt.Errorf("start superseded during previous invocation cleanup")
		}
	}

	if u == nil {
		return fmt.Errorf("unit %q is not loaded", name)
	}
	if u.RequiresInteractiveSession && !m.hasInteractiveSession() {
		return core.ErrSkipped
	}
	if u.Kind == unit.KindTimer {
		return m.armTimer(u, revision)
	}
	if u.Kind == unit.KindRegistry {
		return m.armRegistry(u, revision)
	}
	if u.Kind == unit.KindEventLog {
		return m.armEventLog(u, revision)
	}
	if u.Kind == unit.KindPath {
		return m.armPath(u, revision)
	}
	if u.Kind != unit.KindService || u.Service == nil {
		return nil
	}
	svc := u.Service
	native := svc.Type == unit.TypeSCM || svc.Type == unit.TypeScheduledTask
	m.recordServiceLaunch(effect, native)
	if svc.Type == unit.TypeSCM {
		return m.startSCM(ctx, name, u, autoRestart, effect.owner)
	}
	if svc.Type == unit.TypeScheduledTask {
		return m.startTask(ctx, name, u, autoRestart, effect.owner)
	}
	if err := m.closeNotify(name); err != nil {
		return err
	}
	m.stopWatchdog(name)

	// Resolve prior output before creating a replacement process. A timed-out
	// stream remains owned and requires an explicit cleanup retry.
	if err := m.waitJournalContext(ctx, name, stopTimeout(u)); err != nil {
		return err
	}

	inv := journal.NewInvocationID()
	m.recordInvocation(effect, inv)

	env := journal.InjectEnv(mergeEnv(svc.Environment), inv)
	var nrt *notifyRuntime
	if svc.NeedsNotifyPipe() {
		var err error
		nrt, err = m.openNotify(name)
		if err != nil {
			return errors.Join(err, m.disposeNotify(nrt))
		}
		if !m.adoptLaunchNotify(effect, nrt) {
			return errors.Join(fmt.Errorf("manager closed during notification open"), m.disposeNotify(nrt))
		}
		wd := time.Duration(0)
		if svc.WatchdogMode == unit.WatchdogModeNotify {
			wd = svc.WatchdogSec
		}
		env = notify.Inject(env, nrt.Addr(), wd)
	}

	spec := runtime.StartSpec{
		Unit:         name,
		Type:         svc.Type,
		Argv:         append([]string(nil), svc.ExecStart...),
		Dir:          svc.WorkingDirectory,
		Env:          env,
		TimeoutStart: svc.TimeoutStartSec,
		Limits:       runtime.JobLimitsFromSpec(svc),
	}
	if svc.Type == unit.TypeNotify {
		// TimeoutStartSec bounds READY=1, not CreateProcess.
		spec.TimeoutStart = 0
	}

	proc, err := m.launch.Start(ctx, spec)
	pid := 0
	if proc != nil {
		pid = proc.PID()
	}
	if err != nil {
		if proc != nil {
			// The launcher could not finish cleanup after process creation.
			// This operation retains the record, and the unit lock excludes a
			// replacement. Preserve ownership even if stop/close overtook it.
			m.retainLaunchFailure(effect, proc, pid, err)
			m.attachMainCapture(effect, proc, inv)
		}
		err = errors.Join(err, m.closeNotify(name))
		var st *runtime.ExitStatus
		if proc == nil && errors.As(err, &st) {
			m.maybeRestart(name, classifyWait(err), svc)
		}
		return err
	}
	if nrt != nil {
		nrt.SetMain(proc.PID(), proc.Job())
	}
	m.attachMainCapture(effect, proc, inv)

	alive := false
	if autoRestart && svc.Type != unit.TypeNotify {
		alive = proc.Alive()
	}
	if !m.adoptProcess(processAdoption{effect: effect, process: proc, pid: pid, autoRestart: autoRestart, alive: alive}) {
		stopErr := m.stopProcess(proc, stopTimeout(u))
		if stopErr == nil && proc.Alive() {
			stopErr = fmt.Errorf("process remains alive after late launch cleanup")
		}
		notifyErr := m.closeNotify(name)
		m.applyLateLaunchCleanup(effect, proc, stopErr)
		return errors.Join(fmt.Errorf("start superseded during process creation"), stopErr, notifyErr)
	}
	gen := effect.owner.gen

	if svc.Type == unit.TypeOneshot {
		waitCtx, cancel := m.clockTimeout(ctx, svc.TimeoutStartSec)
		if !m.registerStartWait(effect, cancel) {
			cancel()
		}
		err := proc.Wait(waitCtx)
		if err == nil && jobLimitHit(proc) {
			err = errors.New(core.ReasonResourceLimit)
		}
		if waitCtx.Err() != nil {
			err = fmt.Errorf("TimeoutStartSec exceeded: %w", waitCtx.Err())
		}
		cancel()
		m.clearStartWait(effect)
		if err != nil {
			stopErr := m.stopProcess(proc, stopTimeout(u))
			if stopErr == nil && proc.Alive() {
				stopErr = fmt.Errorf("process remains alive after stop")
			}
			m.applyOneshotCleanup(effect, proc, stopErr, err)
			go m.watch(name, proc)
			if stopErr == nil {
				m.maybeRestart(name, classifyWait(err), svc)
			}
			return errors.Join(err, stopErr)
		}
		var helperErr error
		stopCtx, cancelStop := m.clockTimeout(ctx, stopTimeout(u))
		defer cancelStop()
		if !svc.RemainAfterExit {
			helperErr = m.cooperativeStop(stopCtx, effect.owner, u, proc, true)
		}
		// Drain the final bytes before watch closes the process's pipe handles.
		if job := proc.Job(); job != nil {
			if err := job.Kill(); err != nil {
				m.applyOneshotCleanup(effect, proc, err, nil)
				return err
			}
		}
		journalErr := m.waitJournalContext(stopCtx, name, stopTimeout(u))
		cleanupErr := m.stopProcessContext(stopCtx, proc, stopTimeout(u))
		if cleanupErr == nil && proc.Alive() {
			cleanupErr = fmt.Errorf("process remains alive after oneshot cleanup")
		}
		notifyErr := m.closeNotifyContext(stopCtx, name, stopTimeout(u))
		when, err := m.completeOneshot(ctx, effect, proc, cleanupErr, errors.Join(helperErr, journalErr))
		if err != nil || notifyErr != nil {
			return errors.Join(err, notifyErr)
		}
		if m.engine != nil {
			m.engine.UnitActive(name, when)
		}
		m.maybeRestart(name, core.ExitSuccess, svc)
		return nil
	}

	if svc.Type == unit.TypeNotify {
		if err := m.waitReady(ctx, name, proc, svc.TimeoutStartSec); err != nil {
			stopping := m.acceptReadinessFailure(effect, proc, err)
			stopErr := m.stopProcess(proc, stopTimeout(u))
			if stopErr == nil && proc.Alive() {
				stopErr = fmt.Errorf("process remains alive after readiness cleanup")
			}
			notifyErr := m.closeNotify(name)
			m.applyReadinessCleanup(effect, proc, stopErr)
			if !stopping && stopErr == nil && notifyErr == nil {
				m.maybeRestart(name, core.ExitFailure, svc)
			}
			return errors.Join(err, stopErr, notifyErr)
		}
	}

	if when, accepted := m.acceptProcessActivation(ctx, effect, proc); accepted {
		if m.engine != nil {
			m.engine.UnitActive(name, when)
		}
		if svc.WatchdogEnabled() {
			m.startWatchdog(name, svc, gen)
		}
	}

	go m.watch(name, proc)
	return nil
}

func (m *Manager) watch(name string, proc runtime.Process) {
	// Wait for the main process or a Job Object MemoryMax=/ProcessLimit=
	// hit; then tear down the unit job so leftover children die. Restart
	// CreateProcess into a new per-unit job.
	err, limitHit := waitProcOrLimit(proc)
	unlock := m.ops.lock(name)
	released := false
	release := func() {
		if !released {
			released = true
			unlock()
		}
	}
	defer release()
	effect := m.acceptProcessExitCleanup(name, proc)
	if effect == nil {
		return
	}
	u, wdCancel := effect.unit, effect.cancel
	if wdCancel != nil {
		wdCancel()
	}
	stopCtx, cancelStop := m.clockTimeout(context.Background(), stopTimeout(u))
	defer cancelStop()
	helperErr := m.cooperativeStop(stopCtx, effect.owner, u, proc, effect.stopEligible)
	cleanupErr := m.stopProcessContext(stopCtx, proc, stopTimeout(u))
	if cleanupErr == nil && proc.Alive() {
		cleanupErr = fmt.Errorf("process remains alive after exit cleanup")
	}
	notifyErr := m.closeNotifyContext(stopCtx, name, stopTimeout(u))
	if !m.applyProcessExitCleanup(processExitCleanup{effect: effect, err: cleanupErr}) {
		return
	}
	if notifyErr != nil {
		return
	}
	if helperErr != nil {
		err = errors.Join(err, helperErr)
	}
	// Restart waits and relaunches through the same operation lock.
	release()

	var svc *unit.ServiceSpec
	if u != nil {
		svc = u.Service
	}
	m.applyProcessExit(processExitCompletion{owner: effect.owner, service: svc, waitErr: err, limitHit: limitHit, suppressed: effect.suppressed})
}

func waitProcOrLimit(proc runtime.Process) (error, bool) {
	if proc == nil {
		return nil, false
	}
	errCh := make(chan error, 1)
	go func() { errCh <- proc.Wait(context.Background()) }()
	var limitC <-chan struct{}
	if job := proc.Job(); job != nil {
		limitC = job.ResourceLimitC()
	}
	if limitC == nil {
		err := <-errCh
		return err, jobLimitHit(proc)
	}
	select {
	case err := <-errCh:
		if jobLimitHit(proc) {
			return err, true
		}
		select {
		case <-limitC:
			return err, true
		case <-time.After(150 * time.Millisecond):
			return err, jobLimitHit(proc)
		}
	case <-limitC:
		if job := proc.Job(); job != nil {
			_ = job.Kill()
		}
		select {
		case err := <-errCh:
			return err, true
		case <-time.After(2 * time.Second):
			return fmt.Errorf("resource-limit"), true
		}
	}
}

func jobLimitHit(proc runtime.Process) bool {
	if proc == nil {
		return false
	}
	job := proc.Job()
	return job != nil && job.ResourceLimitHit()
}

func (m *Manager) maybeRestart(name string, kind core.ExitKind, svc *unit.ServiceSpec) {
	if svc == nil || !core.ShouldRestart(svc.Restart, kind) {
		return
	}
	m.mu.Lock()
	rt := m.units[name]
	if m.closed || rt == nil || rt.stopping || rt.unavailable {
		m.mu.Unlock()
		return
	}
	gen := rt.gen
	owner := runtimeIdentity{name: name, record: rt, gen: gen}
	m.mu.Unlock()
	go m.beginRestart(recoveryRequest{owner: owner, delay: restartDelay(svc)})
}

func (m *Manager) beginRestart(request recoveryRequest) {
	name, owner := request.owner.name, request.owner
	work := m.acceptRecovery(request)
	if work == nil {
		return
	}

	ctx := work.Context
	if work.delay > 0 {
		timer := m.clock().Timer(work.delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C():
		}
	} else if ctx.Err() != nil {
		return
	}

	m.mu.Lock()
	rt := m.units[name]
	if m.closed || !owner.currentLocked(m) || rt.stopping || rt.unavailable {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	unlock := m.ops.lock(name)
	defer unlock()
	// Stop, recreation, or another recovery can win while this worker waits
	// for the unit gate. Validate the same owner inside launch admission too.
	_ = m.launchUnitOwnedOp(ctx, name, true, nil, &owner)
}

func (m *Manager) reapFailed(effect failedProcessEffect) {
	unlock := m.ops.lock(effect.owner.name)
	defer unlock()
	if !m.acceptFailedProcessCleanup(effect) {
		return
	}
	err := m.stopProcess(effect.process, defaultStopTimeout)
	if err == nil && effect.process.Alive() {
		err = fmt.Errorf("process remains alive after failed-state cleanup")
	}
	m.applyFailedProcessCleanup(effect, err)
}

func classifyWait(err error) core.ExitKind {
	if err == nil {
		return core.ExitSuccess
	}
	var st *runtime.ExitStatus
	if errors.As(err, &st) {
		if st.SignalEquivalent() {
			return core.ExitAbnormal
		}
		if st.Failed() {
			return core.ExitFailure
		}
		return core.ExitSuccess
	}
	return core.ExitAbnormal
}

func waitFailMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func mainExitMessage(err error) string {
	var st *runtime.ExitStatus
	if errors.As(err, &st) && st.SignalEquivalent() {
		return err.Error()
	}
	return "main process exited"
}

func restartDelay(svc *unit.ServiceSpec) time.Duration {
	if svc != nil && svc.RestartSecSet {
		return svc.RestartSec
	}
	return defaultRestartDelay
}

func stopTimeout(u *unit.Unit) time.Duration {
	if u != nil && u.Service != nil && u.Service.TimeoutStopSecSet {
		return u.Service.TimeoutStopSec
	}
	return defaultStopTimeout
}

func (m *Manager) stopProcess(proc runtime.Process, timeout time.Duration) error {
	return m.stopProcessContext(context.Background(), proc, timeout)
}

func (m *Manager) stopProcessContext(ctx context.Context, proc runtime.Process, timeout time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if proc == nil {
		return nil
	}
	return m.awaitStop(ctx, stopKey{proc: proc}, timeout, func() error { return proc.Stop(timeout) })
}

func (m *Manager) awaitStop(ctx context.Context, key stopKey, timeout time.Duration, stop func() error) error {
	return m.stops.wait(ctx, m.clock(), key, timeout, stop)
}

func mergeEnv(extra []unit.EnvVar) []string {
	base := os.Environ()
	if len(extra) == 0 {
		return base
	}
	index := make(map[string]int, len(base))
	for i, e := range base {
		name, _, _ := strings.Cut(e, "=")
		index[envKey(name)] = i
	}
	for _, v := range extra {
		kv := v.Name + "=" + v.Value
		if i, ok := index[envKey(v.Name)]; ok {
			base[i] = kv
			continue
		}
		index[envKey(v.Name)] = len(base)
		base = append(base, kv)
	}
	return base
}

func envKey(name string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) {
			return unicode.ToUpper(r)
		}
		return r
	}, name)
}

func (m *Manager) hasInteractiveSession() bool {
	if m.cfg.HasInteractiveSession == nil {
		return true
	}
	return m.cfg.HasInteractiveSession()
}

// journalOrigin is user-manager identity for v=2 journal lines.
// System-scope leaves session and user SID empty.
func (m *Manager) journalOrigin() journal.Origin {
	if m == nil || !m.cfg.UserScope {
		return journal.Origin{}
	}
	o := journal.Origin{UserSID: m.cfg.NotifySID}
	if m.cfg.SessionID != nil {
		o.Session = m.cfg.SessionID()
	} else if o.UserSID != "" {
		o.Session = runtime.SIDSession(o.UserSID)
	}
	return o
}
