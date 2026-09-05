package manager

import (
	"context"
	"errors"
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
		return rt.proc == nil && !rt.stopUncertain
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
		return rt.proc == p && rt.stopUncertain && rt.state == core.Failed
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
