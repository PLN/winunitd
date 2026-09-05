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
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
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
		rt.startTimes = nil
	} else if m.startLimitHitLocked(rt) {
		m.failStartLimitLocked(rt)
		m.mu.Unlock()
		return nil
	}
	startGen := rt.gen
	// Dead-but-unreaped: we are removing the proc, so we own job.Kill
	// and Close. Do not rely on watch's early return (issue #25).
	evicted := rt.takeProc()
	u := rt.unit
	m.mu.Unlock()
	teardownJob(evicted)

	if u == nil {
		return fmt.Errorf("unit %q is not loaded", name)
	}
	if u.RequiresInteractiveSession && !m.hasInteractiveSession() {
		return core.ErrSkipped
	}
	if u.Kind == unit.KindTimer {
		m.armTimer(u)
		return nil
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
	m.closeNotify(name)
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
			return err
		}
		m.mu.Lock()
		if rt := m.units[name]; rt != nil && !m.closed {
			rt.notify = nrt
			m.mu.Unlock()
		} else {
			m.mu.Unlock()
			nrt.Close()
			return nil
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
		m.closeNotify(name)
		var st *runtime.ExitStatus
		if errors.As(err, &st) {
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
	rt = m.units[name]
	if rt == nil || m.closed || rt.stopping || rt.gen != startGen {
		m.mu.Unlock()
		_ = proc.Stop(0)
		m.closeNotify(name)
		return nil
	}
	if existing := rt.proc; existing != nil && existing.Alive() {
		m.mu.Unlock()
		_ = proc.Stop(0)
		m.closeNotify(name)
		return nil
	}
	rt.proc = proc
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
				_ = rt.takeProc()
			}
			stopping := rt != nil && rt.stopping
			if rt != nil && !rt.stopping && rt.gen == startGen {
				if rt.step(core.EventStartFailed) {
					rt.err = err.Error()
				}
			}
			m.mu.Unlock()
			_ = proc.Stop(0)
			m.closeNotify(name)
			if !stopping {
				m.maybeRestart(name, core.ExitFailure, svc)
			}
			return err
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
	m.mu.Lock()
	rt := m.units[name]
	if rt == nil || rt.proc != proc {
		m.mu.Unlock()
		// No longer the live proc: still close what we were given if
		// launchUnit (or another owner) has not already (issue #25).
		teardownJob(proc)
		return
	}
	if rt.stopUncertain {
		// A cleanup operation (or its explicit retry) owns this process.
		// Main-process exit alone does not confirm descendant termination;
		// keep the reference until that operation reports success.
		m.mu.Unlock()
		return
	}
	_ = rt.takeProc()
	stopping := rt.stopping
	terminated := rt.terminated
	rt.terminated = false
	u := rt.unit
	gen := rt.gen
	nrt := rt.notify
	rt.notify = nil
	wdCancel := rt.watchdog
	rt.watchdog = nil
	m.mu.Unlock()

	if wdCancel != nil {
		wdCancel()
	}
	if nrt != nil {
		nrt.Close()
	}

	teardownJob(proc)

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
	if proc == nil {
		return nil
	}
	if timeout <= 0 {
		return proc.Stop(timeout)
	}
	done := make(chan error, 1)
	go func() {
		done <- proc.Stop(timeout)
	}()
	t := m.clock().Timer(timeout)
	defer t.Stop()
	select {
	case err := <-done:
		return err
	case <-t.C():
		return fmt.Errorf("TimeoutStopSec exceeded")
	}
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
