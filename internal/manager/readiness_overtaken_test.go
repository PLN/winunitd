package manager

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/notify"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/timers"
)

// aliveHookLauncher runs an armed hook once, inside the next Alive call on a
// launched process. Readiness waits call Alive without the manager lock, so a
// hook can admit a Stop at an exact point of the readiness sequence.
type aliveHookLauncher struct {
	*fakeLauncher
	hook atomic.Pointer[func()]
}

func (l *aliveHookLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	p, err := l.fakeLauncher.Start(ctx, spec)
	if err != nil || p == nil {
		return p, err
	}
	return &aliveHookProcess{Process: p, launch: l}, nil
}

func (l *aliveHookLauncher) arm(hook func()) { l.hook.Store(&hook) }

type aliveHookProcess struct {
	runtime.Process
	launch *aliveHookLauncher
}

func (p *aliveHookProcess) Alive() bool {
	if hook := p.launch.hook.Swap(nil); hook != nil {
		(*hook)()
	}
	return p.Process.Alive()
}

// admitStop starts Stop and returns once the manager has accepted it. Stop
// itself then waits for the unit gate the start still holds.
func admitStop(m *Manager, name string, stopped chan<- error) bool {
	go func() {
		_, err := m.Stop(strings.TrimSuffix(name, ".service"))
		stopped <- err
	}()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		stopping := m.stoppingOfLocked(name)
		m.mu.Unlock()
		if stopping {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

func assertOvertakenStart(t *testing.T, m *Manager, name string, admitted *atomic.Bool, errc <-chan error, stopped <-chan error) {
	t.Helper()
	err := waitErr(t, errc)
	if !admitted.Load() {
		t.Fatal("Stop was not accepted inside the readiness sequence")
	}
	if err == nil {
		t.Fatal("Start succeeded although an accepted Stop overtook its activation")
	}
	if !strings.Contains(err.Error(), "start superseded after readiness") {
		t.Fatalf("Start error = %v, want the refused-activation error", err)
	}
	var perr *protocol.Error
	if !errors.As(err, &perr) || perr.OperationID == "" {
		t.Fatalf("Start error carries no operation: %v", err)
	}
	if op, opErr := m.Operation(perr.OperationID); opErr != nil || op.State != "failed" {
		t.Fatalf("overtaken start operation = %+v, %v; want failed", op, opErr)
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Stop did not return")
	}
	waitState(t, m, name, core.Inactive)
	// The accepted stop keeps its outcome; the start's failure is not published
	// onto the unit.
	status, err := m.Status(strings.TrimSuffix(name, ".service"))
	if err != nil {
		t.Fatal(err)
	}
	if status.Unit.Error != "" {
		t.Fatalf("stopped unit carries the overtaken start's error %q", status.Unit.Error)
	}
}

// stopHookTimer runs a shared one-shot hook when a timer is stopped.
type stopHookTimer struct {
	timers.Timer
	hook *atomic.Pointer[func()]
}

func (t *stopHookTimer) Stop() bool {
	if hook := t.hook.Swap(nil); hook != nil {
		(*hook)()
	}
	return t.Timer.Stop()
}

// READY=1 delivered over the notify endpoint completes waitReady, and Stop is
// accepted before the activation is. The start must fail (#278).
func TestNotifyReadyAfterAcceptedStopFailsStart(t *testing.T) {
	t.Parallel()
	launch := fakeNotifyLaunch()
	clk := timers.NewFake(time.Time{}).Clock()
	newTimer := clk.NewTimer
	var hook atomic.Pointer[func()]
	// Only waitReady's TimeoutStartSec budget lasts 5m; the operation budget is
	// longer. waitReady stops that timer as it returns, after it has selected
	// READY=1 and stopped its periodic stopping check, and before the launch
	// path decides the activation. Stop is accepted there.
	clk.NewTimer = func(d time.Duration) timers.Timer {
		timer := newTimer(d)
		if d != 5*time.Minute {
			return timer
		}
		return &stopHookTimer{Timer: timer, hook: &hook}
	}
	m := managerWithClock(t, launch, clk, map[string]string{"worker.service": `
[Service]
Type=notify
ExecStart=C:\App\worker.exe
WorkingDirectory=C:\App
TimeoutStartSec=5m
`})
	stopped := make(chan error, 1)
	var admitted atomic.Bool
	accept := func() { admitted.Store(admitStop(m, "worker.service", stopped)) }
	hook.Store(&accept)
	errc := make(chan error, 1)
	go func() { _, err := m.Start(context.Background(), "worker"); errc <- err }()
	pipe := waitNotifyPipe(t, launch, "worker.service")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err := notify.Send(ctx, pipe, notify.Message{Ready: true})
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	assertOvertakenStart(t, m, "worker.service", &admitted, errc, stopped)
}

// A readiness probe that succeeds while Stop is being accepted must not
// complete the start (#278).
func TestProbeReadyAfterAcceptedStopFailsStart(t *testing.T) {
	t.Parallel()
	launch := &aliveHookLauncher{fakeLauncher: &fakeLauncher{}}
	stopped := make(chan error, 1)
	var admitted atomic.Bool
	var manager atomic.Pointer[Manager]
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The probe succeeds; the readiness wait checks its context, then calls
		// Alive, where Stop is accepted before the activation is.
		launch.arm(func() { admitted.Store(admitStop(manager.Load(), "app.service", stopped)) })
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	m, _ := managerWithFake(t, launch, map[string]string{"app.service": readinessUnit(server.URL)})
	manager.Store(m)
	errc := make(chan error, 1)
	go func() { _, err := m.Start(context.Background(), "app"); errc <- err }()
	assertOvertakenStart(t, m, "app.service", &admitted, errc, stopped)
}
