package manager

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/timers"
)

type blockedStopProcess struct {
	runtime.Process
	calls   atomic.Int32
	release chan struct{}
}

func (p *blockedStopProcess) Stop(timeout time.Duration) error {
	p.calls.Add(1)
	<-p.release
	return p.Process.Stop(timeout)
}

func TestStopRetryJoinsPendingAdapterCall(t *testing.T) {
	m := &Manager{clk: timers.DefaultClock()}
	p := &blockedStopProcess{Process: &fakeProc{done: make(chan struct{})}, release: make(chan struct{})}
	var once sync.Once
	unblock := func() { once.Do(func() { close(p.release) }) }
	defer unblock()
	for i := 0; i < 3; i++ {
		if err := m.stopProcess(p, 20*time.Millisecond); err == nil {
			t.Fatal("blocked stop reported success")
		}
	}
	if got := p.calls.Load(); got != 1 {
		t.Fatalf("concurrent adapter stops = %d, want 1", got)
	}
	unblock()
	if err := m.stopProcess(p, time.Second); err != nil {
		t.Fatal("stop did not recover", err)
	}
	if p.Alive() {
		t.Fatal("recovered stop left process alive")
	}
}

type blockedSCMStop struct {
	runtime.SCM
	calls   atomic.Int32
	release <-chan struct{}
}

func (s *blockedSCMStop) Stop(ctx context.Context, name string, timeout time.Duration) (runtime.SCMStatus, error) {
	s.calls.Add(1)
	<-s.release
	return s.SCM.Stop(ctx, name, timeout)
}

type blockedTaskStop struct {
	runtime.TaskScheduler
	calls   atomic.Int32
	release <-chan struct{}
}

func (s *blockedTaskStop) Stop(ctx context.Context, name string, timeout time.Duration) (runtime.TaskStatus, error) {
	s.calls.Add(1)
	<-s.release
	return s.TaskScheduler.Stop(ctx, name, timeout)
}

func TestNativeShutdownDeadlineJoinsPendingStop(t *testing.T) {
	for _, kind := range []string{"scm", "scheduled-task"} {
		t.Run(kind, func(t *testing.T) {
			const name = "native-deadline.service"
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			var m *Manager
			var calls *atomic.Int32
			if kind == "scm" {
				s := &blockedSCMStop{SCM: newFakeSCM("example-worker"), release: release}
				calls = &s.calls
				m = managerWithSCM(t, &fakeLauncher{}, s, map[string]string{name: "[Service]\nType=scm\nServiceName=example-worker\nTimeoutStopSec=30s\n"})
			} else {
				s := &blockedTaskStop{TaskScheduler: newFakeTasks("example-worker"), release: release}
				calls = &s.calls
				m = managerWithTasks(t, &fakeLauncher{}, s, map[string]string{name: "[Service]\nType=scheduled-task\nTaskName=example-worker\nTimeoutStopSec=30s\n"})
			}
			if _, err := m.Start(context.Background(), name); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 3; i++ {
				ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
				done := make(chan error, 1)
				go func() { done <- m.Shutdown(ctx) }()
				err := waitErr(t, done)
				cancel()
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("blocked native shutdown = %v", err)
				}
			}
			if n := calls.Load(); n != 1 {
				t.Fatalf("native stop calls = %d, want one pending call", n)
			}
			m.mu.Lock()
			uncertain := m.units[name].cleanupPending()
			m.mu.Unlock()
			if !uncertain {
				t.Fatal("native termination uncertainty discarded")
			}
			unblock()
			if err := m.Shutdown(context.Background()); err != nil {
				t.Fatal("native shutdown retry", err)
			}
			m.mu.Lock()
			uncertain = m.units[name].cleanupPending()
			m.mu.Unlock()
			if uncertain {
				t.Fatal("native cleanup still uncertain after successful retry")
			}
		})
	}
}
