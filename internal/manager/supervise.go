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

func (m *Manager) startOne(ctx context.Context, name string) error {
	return m.launchUnit(ctx, name, false)
}

func (m *Manager) launchUnit(ctx context.Context, name string, autoRestart bool) error {
	unlock := m.ops.lock(name)
	defer unlock()
	return m.launchUnitOp(ctx, name, autoRestart)
}

func (m *Manager) launchUnitOp(ctx context.Context, name string, autoRestart bool) error {
	return m.launchUnitConfigOp(ctx, name, autoRestart, nil)
}

type plannedStart struct {
	origin    activationOrigin
	unit      *unit.Unit
	record    *unitRuntime
	stopEpoch uint64
	launched  bool   // adapter work was admitted before source invalidation
	completed bool   // completion already published while holding the unit gate
	gen       uint64 // generation at plan acceptance, for members never launched
}

// A transaction passes its captured definition; recovery uses ownedUnit.
func (m *Manager) launchUnitConfigOp(ctx context.Context, name string, autoRestart bool, planned *plannedStart) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		if autoRestart {
			return nil
		}
		return fmt.Errorf("manager is shutting down or closed")
	}
	rt := m.units[name]
	if rt == nil {
		m.mu.Unlock()
		if autoRestart {
			return nil
		}
		return fmt.Errorf("unit %q is not loaded", name)
	}
	if autoRestart && rt.stopping {
		m.mu.Unlock()
		return nil
	}
	if planned != nil && (planned.record != rt || planned.stopEpoch != rt.stopEpoch || (planned.origin != nil && !planned.origin.validLocked(m))) {
		m.mu.Unlock()
		return fmt.Errorf("unit %q start superseded by stop", name)
	}
	if rt.stopUncertain {
		m.mu.Unlock()
		return fmt.Errorf("unit %q termination is unconfirmed; retry stop before starting", name)
	}
	// Bump gen only for a real launch. A redundant Start on a live
	// process must not invalidate the running watchdog (issue #23).
	if live := rt.proc; live != nil && live.Alive() {
		m.mu.Unlock()
		return nil
	}
	if rt.unavailable {
		m.mu.Unlock()
		return fmt.Errorf("unit %q has no valid configuration; reload a valid unit before starting", name)
	}
	// Native start failures can still leave an external resource running. Do
	// not abandon its identity when an explicit start adopts a reloaded unit.
	owned := rt.ownedUnit()
	definition := rt.unit
	if planned != nil {
		definition = planned.unit
	}
	if !autoRestart && rt.invocationUnit != nil &&
		(scmServiceName(owned) != "" || scheduledTaskName(owned) != "") &&
		(scmServiceName(owned) != scmServiceName(definition) || scheduledTaskName(owned) != scheduledTaskName(definition)) {
		m.mu.Unlock()
		return fmt.Errorf("unit %q still owns a previous native target; stop it before starting the new definition", name)
	}
	if planned != nil {
		planned.launched = true
	}
	triggered := planned != nil && planned.origin != nil
	if triggered {
		interval, burst := startLimitOf(definition)
		if core.StartLimitHit(rt.startTimes, m.now(), interval, burst) {
			rt.cancelRestart()
			m.failStartLimitLocked(rt)
			m.mu.Unlock()
			return errors.New(core.ReasonStartLimit)
		}
	}
	rt.operations++
	defer func(record *unitRuntime) {
		m.mu.Lock()
		record.operations--
		m.mu.Unlock()
	}(rt)
	if !autoRestart {
		rt.stopping = false
		rt.gen++
		rt.cancelRestart()
		if !triggered {
			rt.startTimes = nil
		}
	} else if m.startLimitHitLocked(rt) {
		m.failStartLimitLocked(rt)
		m.mu.Unlock()
		return nil
	}
	startGen := rt.gen
	// A start can win the operation lock before the exit watcher. Retain the
	// old invocation until cleanup succeeds; a dead main PID is not sufficient.
	evicted := rt.proc
	if evicted != nil {
		rt.stopUncertain = true
	}
	u := definition
	if autoRestart {
		u = owned
	}
	m.mu.Unlock()
	if evicted != nil {
		cleanupErr := m.stopProcess(evicted, stopTimeout(owned))
		if cleanupErr == nil && evicted.Alive() {
			cleanupErr = fmt.Errorf("process remains alive after previous invocation cleanup")
		}
		m.mu.Lock()
		stillOwned := m.units[name] == rt && rt.sameOp(startGen, evicted) && !m.closed
		if stillOwned {
			if cleanupErr == nil {
				rt.proc = nil
				rt.stopUncertain = false
			} else {
				rt.err = fmt.Sprintf("previous invocation cleanup: %v", cleanupErr)
			}
		}
		m.mu.Unlock()
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
		return m.armTimer(u)
	}
	if u.Kind == unit.KindRegistry {
		return m.armRegistry(u)
	}
	if u.Kind == unit.KindEventLog {
		return m.armEventLog(u)
	}
	if u.Kind == unit.KindPath {
		return m.armPath(u)
	}
	if u.Kind != unit.KindService || u.Service == nil {
		return nil
	}
	m.mu.Lock()
	if rt := m.units[name]; rt != nil && rt.gen == startGen && !m.closed {
		rt.invocationUnit = u
		m.recordStartLocked(rt)
	}
	m.mu.Unlock()
	svc := u.Service
	if svc.Type == unit.TypeSCM {
		return m.startSCM(ctx, name, u, autoRestart)
	}
	if svc.Type == unit.TypeScheduledTask {
		return m.startTask(ctx, name, u, autoRestart)
	}
	if err := m.closeNotify(name); err != nil {
		return err
	}
	m.stopWatchdog(name)

	inv := journal.NewInvocationID()
	m.mu.Lock()
	if rt := m.units[name]; rt != nil {
		rt.invocation = inv
	}
	m.mu.Unlock()

	env := journal.InjectEnv(mergeEnv(svc.Environment), inv)
	var nrt *notifyRuntime
	if svc.NeedsNotifyPipe() {
		var err error
		nrt, err = m.openNotify(name)
		if err != nil {
			return errors.Join(err, m.disposeNotify(nrt))
		}
		m.mu.Lock()
		if rt := m.units[name]; rt != nil && !m.closed {
			rt.notify = nrt
			m.mu.Unlock()
		} else {
			m.mu.Unlock()
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
	if err != nil {
		if proc != nil {
			// The launcher could not finish cleanup after process creation.
			// This operation retains the record, and the unit lock excludes a
			// replacement. Preserve ownership even if stop/close overtook it.
			m.mu.Lock()
			rt := m.units[name]
			rt.proc = proc
			rt.stopUncertain = true
			rt.err = err.Error()
			m.mu.Unlock()
			m.journal.SetOrigin(m.journalOrigin())
			m.journal.Attach(name, proc.PID(), inv, proc.Stdout(), proc.Stderr())
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
	// Bound the previous capture wait by TimeoutStopSec so a self-exited
	// unit with a lingering journal cannot hang the next Start or
	// Restart= relaunch (issue #83). Same wait/abandon as stopUnit (#68).
	m.waitJournal(name, stopTimeout(u))
	m.journal.SetOrigin(m.journalOrigin())
	m.journal.Attach(name, proc.PID(), inv, proc.Stdout(), proc.Stderr())

	m.mu.Lock()
	// operations retains this record across reload, and the per-unit operation
	// lock prevents another launch from installing a process. Adopt even a late
	// completion before attempting cleanup so failure remains reachable by Stop.
	rt = m.units[name]
	rt.proc = proc
	if m.closed || rt.stopping || rt.gen != startGen {
		rt.stopUncertain = true
		m.mu.Unlock()
		stopErr := m.stopProcess(proc, stopTimeout(u))
		if stopErr == nil && proc.Alive() {
			stopErr = fmt.Errorf("process remains alive after late launch cleanup")
		}
		stopErr = errors.Join(stopErr, m.closeNotify(name))
		m.mu.Lock()
		if stopErr == nil {
			rt.proc = nil
			rt.stopUncertain = false
		} else {
			rt.step(core.EventStartFailed)
			rt.err = fmt.Sprintf("late launch cleanup: %v", stopErr)
		}
		m.mu.Unlock()
		return errors.Join(fmt.Errorf("start superseded during process creation"), stopErr)
	}
	rt.terminated = false
	if svc.Type == unit.TypeNotify {
		rt.step(core.EventStartRequested)
	} else if autoRestart {
		if proc.Alive() {
			if rt.step(core.EventStartSucceeded) {
				rt.err = ""
			}
		}
	}
	gen := rt.gen
	m.mu.Unlock()

	if svc.Type == unit.TypeOneshot {
		waitCtx, cancel := m.clockTimeout(ctx, svc.TimeoutStartSec)
		m.mu.Lock()
		if rt := m.units[name]; rt != nil && !rt.stopping && !m.closed {
			rt.startCancel = cancel
		} else {
			cancel()
		}
		m.mu.Unlock()
		err := proc.Wait(waitCtx)
		if waitCtx.Err() != nil {
			err = fmt.Errorf("TimeoutStartSec exceeded: %w", waitCtx.Err())
		}
		cancel()
		m.mu.Lock()
		if rt := m.units[name]; rt != nil && rt.gen == gen {
			rt.startCancel = nil
		}
		m.mu.Unlock()
		if err != nil {
			stopErr := m.stopProcess(proc, stopTimeout(u))
			if stopErr == nil && proc.Alive() {
				stopErr = fmt.Errorf("process remains alive after stop")
			}
			m.mu.Lock()
			if rt := m.units[name]; rt != nil && rt.proc == proc {
				rt.terminated = true
				rt.stopUncertain = stopErr != nil
			}
			m.mu.Unlock()
			go m.watch(name, proc)
			if stopErr == nil {
				m.maybeRestart(name, classifyWait(err), svc)
			}
			return errors.Join(err, stopErr)
		}
		// Drain the final bytes before watch closes the process's pipe handles.
		if job := proc.Job(); job != nil {
			if err := job.Kill(); err != nil {
				m.mu.Lock()
				if rt := m.units[name]; rt != nil && rt.proc == proc {
					rt.stopUncertain = true
				}
				m.mu.Unlock()
				return err
			}
		}
		m.waitJournal(name, stopTimeout(u))
	}

	if svc.Type == unit.TypeNotify {
		if err := m.waitReady(ctx, name, proc, svc.TimeoutStartSec); err != nil {
			m.mu.Lock()
			rt = m.units[name]
			if rt != nil && rt.proc == proc {
				rt.terminated = true
				rt.stopUncertain = true
			}
			stopping := rt != nil && rt.stopping
			if rt != nil && !rt.stopping && rt.gen == startGen {
				if rt.step(core.EventStartFailed) {
					rt.err = err.Error()
				}
			}
			m.mu.Unlock()
			stopErr := m.stopProcess(proc, stopTimeout(u))
			if stopErr == nil && proc.Alive() {
				stopErr = fmt.Errorf("process remains alive after readiness cleanup")
			}
			stopErr = errors.Join(stopErr, m.closeNotify(name))
			m.mu.Lock()
			if rt := m.units[name]; rt != nil && rt.sameOp(startGen, proc) {
				if stopErr == nil {
					rt.proc = nil
					rt.stopUncertain = false
					rt.terminated = false
				} else {
					rt.err = fmt.Sprintf("readiness cleanup: %v", stopErr)
				}
			}
			m.mu.Unlock()
			if !stopping && stopErr == nil {
				m.maybeRestart(name, core.ExitFailure, svc)
			}
			return errors.Join(err, stopErr)
		}
		m.mu.Lock()
		if rt := m.units[name]; rt != nil && !rt.stopping && rt.gen == startGen && rt.proc == proc {
			if rt.step(core.EventStartSucceeded) {
				rt.err = ""
			}
		}
		m.mu.Unlock()
	}

	if m.engine != nil {
		m.engine.UnitActive(name, m.now())
	}

	if svc.WatchdogEnabled() {
		m.startWatchdog(name, svc, gen)
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
	m.mu.Lock()
	rt := m.units[name]
	if rt == nil || rt.proc != proc {
		m.mu.Unlock()
		// Ownership only moves after confirmed cleanup. A stale watcher must
		// not repeat termination/handle closure after another operation did it.
		return
	}
	if rt.stopUncertain {
		// A cleanup operation (or its explicit retry) owns this process.
		// Main-process exit alone does not confirm descendant termination;
		// keep the reference until that operation reports success.
		m.mu.Unlock()
		return
	}
	rt.stopUncertain = true
	stopping := rt.stopping
	terminated := rt.terminated
	rt.terminated = false
	u := rt.ownedUnit()
	gen := rt.gen
	wdCancel := rt.watchdog
	rt.watchdog = nil
	m.mu.Unlock()

	if wdCancel != nil {
		wdCancel()
	}
	notifyErr := m.closeNotify(name)
	cleanupErr := m.stopProcess(proc, stopTimeout(u))
	if cleanupErr == nil && proc.Alive() {
		cleanupErr = fmt.Errorf("process remains alive after exit cleanup")
	}
	cleanupErr = errors.Join(cleanupErr, notifyErr)
	m.mu.Lock()
	rt = m.units[name]
	if rt == nil || !rt.sameOp(gen, proc) || rt.proc != proc {
		m.mu.Unlock()
		return
	}
	if cleanupErr != nil {
		if rt.state != core.Failed {
			rt.step(core.EventStartFailed)
		}
		rt.err = fmt.Sprintf("exit cleanup: %v", cleanupErr)
		m.mu.Unlock()
		return
	}
	rt.proc = nil
	rt.stopUncertain = false
	m.mu.Unlock()
	// Restart waits and relaunches through the same operation lock.
	release()

	if stopping || terminated {
		return
	}

	var svc *unit.ServiceSpec
	if u != nil {
		svc = u.Service
	}
	kind := classifyWait(err)
	if limitHit {
		kind = core.ExitResourceLimit
	}
	if svc != nil && core.ShouldRestart(svc.Restart, kind) {
		m.beginRestart(name, gen, restartDelay(svc))
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	rt = m.units[name]
	if rt == nil || rt.stopping || rt.gen != gen {
		return
	}
	if rt.sub == core.SubWatchdog {
		return
	}
	oneshot := svc != nil && svc.Type == unit.TypeOneshot
	if oneshot && kind == core.ExitSuccess {
		return
	}
	if rt.step(core.EventMainExited) {
		if limitHit {
			rt.err = core.ReasonResourceLimit
		} else {
			rt.err = mainExitMessage(err)
		}
	}
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
	m.mu.Unlock()
	go m.beginRestart(name, gen, restartDelay(svc))
}

func (m *Manager) beginRestart(name string, gen uint64, delay time.Duration) {
	m.mu.Lock()
	rt := m.units[name]
	if m.closed || rt == nil || rt.stopping || rt.unavailable || rt.gen != gen {
		m.mu.Unlock()
		return
	}
	if m.startLimitHitLocked(rt) {
		m.failStartLimitLocked(rt)
		m.mu.Unlock()
		return
	}
	if rt.step(core.EventAutoRestart) {
		rt.err = ""
	}
	ctx, cancel := context.WithCancel(context.Background())
	rt.cancelRestart()
	rt.restartCancel = cancel
	m.mu.Unlock()

	if delay > 0 {
		timer := m.clock().Timer(delay)
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
	rt = m.units[name]
	if m.closed || rt == nil || rt.stopping || rt.gen != gen {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	_ = m.launchUnit(context.Background(), name, true)
}

func (m *Manager) subOfLocked(name string) core.Substate {
	if rt := m.units[name]; rt != nil {
		return rt.sub
	}
	return core.SubNone
}

func (m *Manager) reapFailedLocked() {
	for name, rt := range m.units {
		if rt == nil || rt.state != core.Failed || rt.proc == nil || rt.stopUncertain {
			continue
		}
		proc := rt.proc
		gen := rt.gen
		rt.stopUncertain = true
		go m.reapFailed(name, proc, gen)
	}
}

func (m *Manager) reapFailed(name string, proc runtime.Process, gen uint64) {
	unlock := m.ops.lock(name)
	defer unlock()
	m.mu.Lock()
	rt := m.units[name]
	if rt == nil || !rt.sameOp(gen, proc) || rt.proc != proc || m.closed {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	err := m.stopProcess(proc, defaultStopTimeout)
	if err == nil && proc.Alive() {
		err = fmt.Errorf("process remains alive after failed-state cleanup")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rt = m.units[name]
	if rt == nil || !rt.sameOp(gen, proc) || rt.proc != proc {
		return
	}
	if err != nil {
		rt.err = fmt.Sprintf("failed-state cleanup: %v", err)
		return
	}
	rt.proc = nil
	rt.stopUncertain = false
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
