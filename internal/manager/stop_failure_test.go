package manager

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/runtime"
)

type failedStopLauncher struct {
	fakeLauncher
	proc *failedStopProcess
	lie  bool
}

type failedStopProcess struct {
	runtime.Process
	fail    atomic.Bool
	lie     bool
	attempt chan struct{}
}

func (l *failedStopLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	p, err := l.fakeLauncher.Start(ctx, spec)
	if err != nil {
		return nil, err
	}
	l.proc = &failedStopProcess{Process: p, lie: l.lie}
	l.proc.fail.Store(true)
	return l.proc, nil
}

func (p *failedStopProcess) Stop(timeout time.Duration) error {
	if p.attempt != nil {
		defer func() {
			select {
			case p.attempt <- struct{}{}:
			default:
			}
		}()
	}
	if p.fail.Load() {
		if p.lie {
			return nil
		}
		return errors.New("injected termination failure")
	}
	return p.Process.Stop(timeout)
}

func TestFailedStopRetainsOwnershipAndRejectsStart(t *testing.T) {
	for _, lie := range []bool{false, true} {
		name := "failure"
		if lie {
			name = "success-with-live-process"
		}
		t.Run(name, func(t *testing.T) {
			l := &failedStopLauncher{lie: lie}
			m := managerWith(t, l, map[string]string{"worker.service": "[Service]\nType=simple\nExecStart=C:\\Tools\\worker.exe\n"})
			if _, err := m.Start(context.Background(), "worker.service"); err != nil {
				t.Fatal(err)
			}
			p := l.proc
			t.Cleanup(func() { p.fail.Store(false); _ = p.Process.Stop(time.Second) })
			if _, err := m.Stop("worker.service"); err == nil {
				t.Error("unconfirmed termination reported success")
			}
			m.mu.Lock()
			owned := m.procOfLocked("worker.service") == p
			m.mu.Unlock()
			if !owned || !p.Alive() {
				t.Fatal("failed stop lost the live process")
			}
			if _, err := m.Start(context.Background(), "worker.service"); err == nil {
				t.Error("start admitted with uncertain termination")
			}
			if len(l.specs()) != 1 {
				t.Fatal("replacement launched after failed stop")
			}
			m.mu.Lock()
			retained := m.procOfLocked("worker.service") == p
			m.mu.Unlock()
			if !retained {
				t.Fatal("rejected start discarded uncertain process")
			}
			p.fail.Store(false)
			if _, err := m.Stop("worker.service"); err != nil {
				t.Fatal("stop retry failed", err)
			}
			if p.Alive() {
				t.Fatal("retry left process alive")
			}
		})
	}
}

func TestFailedStateReaperRetainsProcessOnStopFailure(t *testing.T) {
	l := &failedStopLauncher{}
	m := managerWith(t, l, map[string]string{"worker.service": "[Service]\nType=simple\nExecStart=C:\\Tools\\worker.exe\n"})
	if _, err := m.Start(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	p := l.proc
	t.Cleanup(func() { p.fail.Store(false); _ = p.Process.Stop(time.Second) })
	p.attempt = make(chan struct{}, 1)
	m.mu.Lock()
	m.units["worker.service"].state = core.Failed
	m.reapFailedLocked()
	m.mu.Unlock()
	select {
	case <-p.attempt:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not attempt termination")
	}
	m.mu.Lock()
	retained := m.procOfLocked("worker.service") == p
	m.mu.Unlock()
	if !retained || !p.Alive() {
		t.Fatal("failed-state cleanup discarded a live process")
	}
	// Even a later main-process exit must not discard uncertain job cleanup.
	p.Process.(*fakeProc).finish()
	m.watch("worker.service", p)
	m.mu.Lock()
	retained = m.procOfLocked("worker.service") == p
	m.mu.Unlock()
	if !retained {
		t.Fatal("main-process exit discarded unresolved cleanup ownership")
	}
	if _, err := m.Start(context.Background(), "worker"); err == nil {
		t.Fatal("replacement admitted after cleanup failure")
	}
	p.fail.Store(false)
	if _, err := m.Stop("worker"); err != nil {
		t.Fatal(err)
	}
	if p.Alive() {
		t.Fatal("explicit retry did not stop retained process")
	}
}

func TestSuccessfulFailedStateCleanupAllowsStart(t *testing.T) {
	l := &fakeLauncher{}
	m := managerWith(t, l, map[string]string{"worker.service": "[Service]\nType=simple\nExecStart=C:\\Tools\\worker.exe\n"})
	if _, err := m.Start(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.units["worker.service"].state = core.Failed
	m.reapFailedLocked()
	m.mu.Unlock()
	waitUntil(t, time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		rt := m.units["worker.service"]
		return rt.proc == nil && !rt.cleanupPending()
	})
	if _, err := m.Start(context.Background(), "worker"); err != nil {
		t.Fatal("start rejected after confirmed cleanup", err)
	}
	if len(l.specs()) != 2 {
		t.Fatal("replacement did not launch after confirmed cleanup")
	}
}

func TestMainExitCleanupFailureRetainsOwnership(t *testing.T) {
	l := &failedStopLauncher{}
	m := managerWith(t, l, map[string]string{"worker.service": "[Service]\nType=simple\nExecStart=C:\\Tools\\worker.exe\nRestart=always\nRestartSec=0\n"})
	if _, err := m.Start(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	p := l.proc
	t.Cleanup(func() { p.fail.Store(false); _ = p.Process.Stop(time.Second) })
	p.Process.(*fakeProc).finish()
	waitUntil(t, time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		rt := m.units["worker.service"]
		return rt.proc == p && rt.cleanupPending() && rt.state == core.Failed
	})
	if len(l.specs()) != 1 {
		t.Fatal("restarted before exit cleanup succeeded")
	}
	if _, err := m.Start(context.Background(), "worker"); err == nil {
		t.Fatal("start admitted after exit cleanup failure")
	}
	p.fail.Store(false)
	if _, err := m.Stop("worker"); err != nil {
		t.Fatal(err)
	}
}

func TestStartCannotDiscardUnreapedProcessAfterCleanupFailure(t *testing.T) {
	l := &failedStopLauncher{}
	m := managerWith(t, l, map[string]string{"worker.service": "[Service]\nType=simple\nExecStart=C:\\Tools\\worker.exe\n"})
	if _, err := m.Start(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	p := l.proc
	t.Cleanup(func() { p.fail.Store(false); _ = p.Process.Stop(time.Second) })
	// Hold the operation lock so the explicit start wins against the exit watcher.
	unlock := m.ops.lock("worker.service")
	p.Process.(*fakeProc).finish()
	err := m.launchUnitOp(context.Background(), "worker.service", false)
	unlock()
	if err == nil {
		t.Fatal("start ignored cleanup failure")
	}
	m.mu.Lock()
	retained := m.procOfLocked("worker.service") == p
	m.mu.Unlock()
	if !retained || len(l.specs()) != 1 {
		t.Fatal("start replaced unresolved invocation")
	}
	p.fail.Store(false)
	if _, err := m.Stop("worker"); err != nil {
		t.Fatal(err)
	}
}

func TestWatchdogCleanupFailureBlocksRestart(t *testing.T) {
	const name = "cleanup-watchdog.service"
	l := &failedStopLauncher{}
	m := managerWith(t, l, map[string]string{name: "[Service]\nType=simple\nExecStart=C:\\Tools\\worker.exe\nWatchdogSec=1h\nRestart=always\nRestartSec=0\n"})
	if _, err := m.Start(context.Background(), name); err != nil {
		t.Fatal(err)
	}
	p := l.proc
	t.Cleanup(func() { p.fail.Store(false); _ = p.Process.Stop(time.Second) })
	m.mu.Lock()
	gen := m.units[name].gen
	owner := runtimeIdentity{name: name, record: m.units[name], gen: gen}
	m.mu.Unlock()
	m.onWatchdogTimeout(owner)
	m.mu.Lock()
	rt := m.units[name]
	retained := rt.proc == p && rt.cleanupPending() && rt.state == core.Failed
	m.mu.Unlock()
	if !retained || len(l.specs()) != 1 {
		t.Fatal("watchdog failed to retain unresolved termination without restart")
	}
	p.fail.Store(false)
	if _, err := m.Stop(name); err != nil {
		t.Fatal(err)
	}
}

func TestReadinessTimeoutCleanupFailureRetainsOwnership(t *testing.T) {
	const name = "cleanup-ready.service"
	l := &failedStopLauncher{}
	m := managerWith(t, l, map[string]string{name: "[Service]\nType=notify\nExecStart=C:\\Tools\\worker.exe\nTimeoutStartSec=20ms\n"})
	if _, err := m.Start(context.Background(), name); err == nil {
		t.Fatal("missing readiness reported success")
	}
	p := l.proc
	t.Cleanup(func() { p.fail.Store(false); _ = p.Process.Stop(time.Second) })
	m.mu.Lock()
	rt := m.units[name]
	retained := rt.proc == p && rt.cleanupPending() && rt.state == core.Failed
	m.mu.Unlock()
	if !retained || !p.Alive() {
		t.Fatal("readiness timeout discarded unconfirmed process")
	}
	p.fail.Store(false)
	if _, err := m.Stop(name); err != nil {
		t.Fatal(err)
	}
}

// A process may already exist when cancellation overtakes the launch response.
type lateFailedStopLauncher struct {
	failedStopLauncher
	entered chan struct{}
	release chan struct{}
}

func (l *lateFailedStopLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	p, err := l.failedStopLauncher.Start(ctx, spec)
	close(l.entered)
	<-l.release
	return p, err
}

func TestLateLaunchCleanupFailureRetainsOwnership(t *testing.T) {
	for _, closing := range []bool{false, true} {
		t.Run(map[bool]string{false: "stop", true: "close"}[closing], func(t *testing.T) {
			const name = "late-cleanup.service"
			l := &lateFailedStopLauncher{entered: make(chan struct{}), release: make(chan struct{})}
			m := managerWith(t, l, map[string]string{name: "[Service]\nType=simple\nExecStart=C:\\Tools\\worker.exe\n"})
			var once sync.Once
			release := func() { once.Do(func() { close(l.release) }) }
			t.Cleanup(release)
			done := make(chan error, 1)
			go func() { _, err := m.Start(context.Background(), name); done <- err }()
			select {
			case <-l.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("launch did not enter")
			}
			p := l.proc
			t.Cleanup(func() { p.fail.Store(false); _ = p.Process.Stop(time.Second) })
			var stopped chan error
			if closing {
				m.Close()
			} else {
				stopped = make(chan error, 1)
				go func() { _, err := m.Stop(name); stopped <- err }()
				waitCond(t, func() bool {
					m.mu.Lock()
					defer m.mu.Unlock()
					return m.units[name].stopping
				})
			}
			release()
			if err := waitErr(t, done); err == nil {
				t.Fatal("superseded launch reported success")
			} else {
				waitOperationErrorCompleted(t, m, err)
			}
			if stopped != nil && waitErr(t, stopped) == nil {
				t.Fatal("failed late-process stop reported success")
			}
			m.mu.Lock()
			rt := m.units[name]
			retained := rt.proc == p && rt.cleanupPending()
			m.mu.Unlock()
			if !retained || !p.Alive() {
				t.Fatal("late launch lost unresolved process ownership")
			}
			status, err := m.Status(name)
			if err != nil || status.Unit == nil {
				t.Fatalf("late process lost status: %v", err)
			}
			p.fail.Store(false)
			if _, err := m.Stop(name); err != nil {
				t.Fatal(err)
			}
			if p.Alive() {
				t.Fatal("successful retry left process alive")
			}
		})
	}
}

type partialStartLauncher struct{ failedStopLauncher }

type bootstrapCleanupProcess struct{ runtime.Process }

func (*bootstrapCleanupProcess) PID() int    { return 0 }
func (*bootstrapCleanupProcess) Alive() bool { return false }

type bootstrapCleanupLauncher struct {
	failedStopLauncher
	owner *bootstrapCleanupProcess
}

func (l *bootstrapCleanupLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	p, err := l.failedStopLauncher.Start(ctx, spec)
	if err != nil {
		return p, err
	}
	l.owner = &bootstrapCleanupProcess{Process: p}
	return l.owner, errors.New("injected bootstrap failure before process creation")
}

func TestFailedBootstrapWithoutPIDRemainsObservableAndRetryable(t *testing.T) {
	const name = "bootstrap.service"
	l := &bootstrapCleanupLauncher{}
	m := managerWith(t, l, map[string]string{name: "[Service]\nExecStart=C:\\Tools\\worker.exe\nRestart=always\n"})
	if _, err := m.Start(context.Background(), name); err == nil {
		t.Fatal("bootstrap failure reported success")
	}
	t.Cleanup(func() { l.proc.fail.Store(false); _ = l.proc.Process.Stop(time.Second) })
	status, err := m.Status(name)
	if err != nil || status.Unit.MainPID != 0 || !status.Unit.TerminationUncertain {
		t.Fatalf("bootstrap owner missing from status: %+v, %v", status, err)
	}
	if _, err := m.Start(context.Background(), name); err == nil {
		t.Fatal("replacement admitted over failed bootstrap cleanup")
	}
	m.mu.Lock()
	retained := m.units[name].proc == l.owner && m.units[name].cleanupPending()
	m.mu.Unlock()
	if !retained || len(l.specs()) != 1 {
		t.Fatal("bootstrap cleanup ownership was replaced")
	}
	l.proc.fail.Store(false)
	if _, err := m.Stop(name); err != nil {
		t.Fatal("bootstrap stop retry", err)
	}
	status, err = m.Status(name)
	if err != nil || status.Unit.TerminationUncertain {
		t.Fatalf("bootstrap cleanup did not complete: %+v, %v", status, err)
	}
}

func (l *partialStartLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	p, err := l.failedStopLauncher.Start(ctx, spec)
	if err != nil {
		return p, err
	}
	return p, errors.New("injected launch failure with unfinished cleanup")
}

func TestFailedLaunchRetainsReturnedProcess(t *testing.T) {
	const name = "partial-start.service"
	l := &partialStartLauncher{}
	m := managerWith(t, l, map[string]string{name: "[Service]\nExecStart=C:\\Tools\\worker.exe\nRestart=always\n"})
	if _, err := m.Start(context.Background(), name); err == nil {
		t.Fatal("failed launch reported success")
	}
	p := l.proc
	t.Cleanup(func() { p.fail.Store(false); _ = p.Process.Stop(time.Second) })
	m.mu.Lock()
	rt := m.units[name]
	retained := rt.proc == p && rt.cleanupPending()
	m.mu.Unlock()
	if !retained || !p.Alive() {
		t.Fatal("failed launch discarded its returned process")
	}
	if _, err := m.Status(name); err != nil {
		t.Fatal("failed launch lost status", err)
	}
	if _, err := m.Start(context.Background(), name); err == nil {
		t.Fatal("replacement admitted with unresolved launch cleanup")
	}
	if len(l.specs()) != 1 {
		t.Fatal("failed launch created a replacement")
	}
	p.fail.Store(false)
	if _, err := m.Stop(name); err != nil {
		t.Fatal("cleanup retry", err)
	}
}
