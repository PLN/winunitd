package manager

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/pathwatch"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/timers"
)

func waitOperationErrorCompleted(t *testing.T, m *Manager, err error) *protocol.OperationResult {
	t.Helper()
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.OperationID == "" {
		t.Fatalf("error lost accepted operation identity: %v", err)
	}
	var op *protocol.OperationResult
	waitCond(t, func() bool {
		var queryErr error
		op, queryErr = m.Operation(pe.OperationID)
		return queryErr == nil && op.State != "running"
	})
	return op
}

func TestFirstCallerCancellationOnlyEndsOperationWait(t *testing.T) {
	launch := &reloadDelayedLauncher{entered: make(chan struct{}), release: make(chan struct{})}
	m := managerWith(t, launch, map[string]string{"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n"})
	var once sync.Once
	release := func() { once.Do(func() { close(launch.release) }) }
	t.Cleanup(release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := m.Start(ctx, "work"); done <- err }()
	select {
	case <-launch.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("start was not accepted")
	}
	cancel()
	err := waitErr(t, done)
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.OperationID == "" {
		t.Fatalf("canceled caller lost operation ID: %v", err)
	}
	op, err := m.Operation(pe.OperationID)
	if err != nil || op.State != "running" || op.CancellationReason != "" || op.DeadlineAt == "" {
		t.Fatalf("caller canceled accepted operation: %+v %v", op, err)
	}
	release()
	waitCond(t, func() bool { op, _ := m.Operation(pe.OperationID); return op.State == "succeeded" })
	assertState(t, m, "work.service", core.Active)
}

// Deliberately returns a live process after cancellation, like an uncancellable
// native create call. Only the operation may adopt and clean up this completion.
type lateOperationLauncher struct {
	fakeLauncher
	entered chan struct{}
	release chan struct{}
}

func (l *lateOperationLauncher) Start(_ context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	close(l.entered)
	<-l.release
	return l.fakeLauncher.Start(context.Background(), spec)
}

func TestOperationDeadlineRetainsLateLaunchAndAdmission(t *testing.T) {
	launch := &lateOperationLauncher{entered: make(chan struct{}), release: make(chan struct{})}
	m, clock := managerWithFake(t, launch, map[string]string{
		"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\nRestart=always\n",
		"other.target": "[Unit]\nDescription=Independent\n",
	})
	m.cfg.OperationTimeout = time.Second
	m.cfg.MaxStartTransactions = 1
	var once sync.Once
	release := func() { once.Do(func() { close(launch.release) }) }
	t.Cleanup(release)
	done := make(chan error, 1)
	go func() { _, err := m.Start(context.Background(), "work"); done <- err }()
	select {
	case <-launch.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("launch did not enter")
	}
	clock.Advance(time.Second)
	err := waitErr(t, done)
	var pe *protocol.Error
	if !errors.As(err, &pe) || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("deadline did not end response wait: %v", err)
	}
	op, _ := m.Operation(pe.OperationID)
	if op.State != "running" || !strings.Contains(op.CancellationReason, "deadline") {
		t.Fatal("unreturned native call must remain owned with visible cancellation")
	}
	if _, err := m.Start(context.Background(), "other.target"); !errors.Is(err, errStartCapacity) {
		t.Fatalf("deadline prematurely released worker admission: %v", err)
	}
	release()
	op = waitOperationErrorCompleted(t, m, err)
	if op.State != "failed" || !strings.Contains(op.Error, "deadline") {
		t.Fatalf("deadline outcome lost: %+v", op)
	}
	m.mu.Lock()
	proc, uncertain, restarting := m.units["work.service"].proc, m.units["work.service"].stopUncertain, m.units["work.service"].sub == core.SubAutoRestart
	m.mu.Unlock()
	if proc != nil || uncertain || restarting {
		t.Fatal("late process survived cleanup or armed recovery")
	}
	if _, err := m.Start(context.Background(), "other.target"); err != nil {
		t.Fatal("completed cleanup did not release admission", err)
	}
}

func TestOperationDeadlineCancelsQueuedUnitGate(t *testing.T) {
	launch := &fakeLauncher{}
	m, clock := managerWithFake(t, launch, map[string]string{"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n"})
	m.cfg.OperationTimeout = time.Second
	unlock := m.ops.lock("work.service")
	defer unlock()
	done := make(chan error, 1)
	go func() { _, err := m.Start(context.Background(), "work"); done <- err }()
	waitCond(t, func() bool { m.ops.mu.Lock(); defer m.ops.mu.Unlock(); return m.ops.by["work.service"].refs == 2 })
	clock.Advance(time.Second)
	err := waitErr(t, done)
	op := waitOperationErrorCompleted(t, m, err)
	if op.State != "failed" || len(launch.units()) != 0 {
		t.Fatal("expired queued operation launched or reported success")
	}
}

func TestCanceledBeforeAdmissionHasNoOperation(t *testing.T) {
	m := managerWith(t, &fakeLauncher{}, map[string]string{"work.target": "[Unit]\nDescription=Work\n"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, call := range []func() (*protocol.UnitResult, error){
		func() (*protocol.UnitResult, error) { return m.Start(ctx, "work.target") },
		func() (*protocol.UnitResult, error) { return m.Restart(ctx, "work.target") },
		func() (*protocol.UnitResult, error) { return m.StopContext(ctx, "work.target") },
	} {
		_, err := call()
		var pe *protocol.Error
		if !errors.As(err, &pe) || pe.OperationID != "" {
			t.Fatalf("canceled request admitted: %v", err)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.operations) != 0 || m.activeStarts != 0 || m.activeStops != 0 {
		t.Fatal("canceled requests consumed admission/history")
	}
}

type operationStopLauncher struct {
	fakeLauncher
	release chan struct{}
	proc    *blockedStopProcess
}

func (l *operationStopLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	p, err := l.fakeLauncher.Start(ctx, spec)
	if err != nil {
		return nil, err
	}
	l.proc = &blockedStopProcess{Process: p, release: l.release}
	return l.proc, nil
}

func TestStopAndRestartOwnDeadlineAndCallerLifetime(t *testing.T) {
	for _, action := range []string{"stop", "restart"} {
		for _, cancellation := range []string{"caller", "deadline"} {
			t.Run(action+"/"+cancellation, func(t *testing.T) {
				launch := &operationStopLauncher{release: make(chan struct{})}
				m, clock := managerWithFake(t, launch, map[string]string{"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\nTimeoutStopSec=30s\n"})
				m.cfg.OperationTimeout = time.Second
				var once sync.Once
				release := func() { once.Do(func() { close(launch.release) }) }
				t.Cleanup(release)
				if _, err := m.Start(context.Background(), "work"); err != nil {
					t.Fatal(err)
				}
				old := launch.proc
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan error, 1)
				go func() {
					var err error
					if action == "stop" {
						_, err = m.StopContext(ctx, "work")
					} else {
						_, err = m.Restart(ctx, "work")
					}
					done <- err
				}()
				waitCond(t, func() bool { return old.calls.Load() == 1 })
				if cancellation == "caller" {
					cancel()
				} else {
					clock.Advance(time.Second)
				}
				err := waitErr(t, done)
				if err == nil {
					t.Fatal("interrupted waiter reported success")
				}
				if cancellation == "deadline" {
					op := waitOperationErrorCompleted(t, m, err)
					if op.State != "failed" || !strings.Contains(op.CancellationReason, "deadline") {
						t.Fatalf("lost deadline: %+v", op)
					}
					if _, startErr := m.Start(context.Background(), "work"); startErr == nil {
						t.Fatal("unresolved stop admitted replacement")
					}
				}
				if old.calls.Load() != 1 {
					t.Fatal("pending native cleanup was duplicated")
				}
				release()
				op := waitOperationErrorCompleted(t, m, err)
				if cancellation == "caller" && op.State != "succeeded" {
					t.Fatalf("caller canceled accepted %s: %+v", action, op)
				}
				wantStarts := 1
				if action == "restart" && cancellation == "caller" {
					wantStarts++
				}
				if got := len(launch.units()); got != wantStarts {
					t.Fatalf("launches=%d want=%d", got, wantStarts)
				}
				if _, err := m.Stop("work"); err != nil {
					t.Fatal("cleanup retry failed", err)
				}
				if old.Alive() {
					t.Fatal("retry lost pending cleanup")
				}
			})
		}
	}
}

func TestAcceptedOperationSurvivesServingContextCancellation(t *testing.T) {
	launch := &reloadDelayedLauncher{entered: make(chan struct{}), release: make(chan struct{})}
	m := managerWith(t, launch, map[string]string{"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n"})
	var once sync.Once
	release := func() { once.Do(func() { close(launch.release) }) }
	t.Cleanup(release)
	server, client := net.Pipe()
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan struct{})
	go func() {
		defer close(served)
		defer server.Close()
		protocol.ServeConn(ctx, server, m, protocol.AllowAdmin)
	}()
	done := make(chan error, 1)
	go func() { _, err := protocol.NewClient(client).Start(context.Background(), "work"); done <- err }()
	select {
	case <-launch.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("RPC operation was not accepted")
	}
	st, err := m.Status("work")
	if err != nil {
		t.Fatal(err)
	}
	id := st.Unit.LastOperationID
	cancel()
	if err := waitErr(t, done); err == nil {
		t.Fatal("canceled serving connection received success")
	}
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("serving goroutine retained the operation wait")
	}
	release()
	waitCond(t, func() bool { op, _ := m.Operation(id); return op.State == "succeeded" })
	assertState(t, m, "work.service", core.Active)
}

type phasedOperationLauncher struct {
	fakeLauncher
	entered chan string
	release map[string]chan struct{}
}

type phasedOperationProcess struct {
	runtime.Process
	name  string
	owner *phasedOperationLauncher
}

func (l *phasedOperationLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	p, err := l.fakeLauncher.Start(ctx, spec)
	if err != nil {
		return nil, err
	}
	return &phasedOperationProcess{Process: p, name: spec.Unit, owner: l}, nil
}

func (p *phasedOperationProcess) Stop(timeout time.Duration) error {
	select {
	case p.owner.entered <- p.name:
	default:
	}
	<-p.owner.release[p.name]
	return p.Process.Stop(timeout)
}

func TestStopOperationSharesDeadlineAcrossOrderedMembers(t *testing.T) {
	launch := &phasedOperationLauncher{entered: make(chan string, 8), release: map[string]chan struct{}{
		"a.service": make(chan struct{}), "b.service": make(chan struct{}),
	}}
	m, clock := managerWithFake(t, launch, map[string]string{
		"group.target": "[Unit]\nWants=a.service b.service\n",
		"a.service":    "[Unit]\nPartOf=group.target\nAfter=b.service\n[Service]\nExecStart=C:\\Tools\\a.exe\nTimeoutStopSec=30s\n",
		"b.service":    "[Unit]\nPartOf=group.target\n[Service]\nExecStart=C:\\Tools\\b.exe\nTimeoutStopSec=30s\n",
	})
	m.cfg.OperationTimeout = time.Second
	var first, second sync.Once
	releaseA := func() { first.Do(func() { close(launch.release["a.service"]) }) }
	releaseB := func() { second.Do(func() { close(launch.release["b.service"]) }) }
	t.Cleanup(releaseA)
	t.Cleanup(releaseB)
	if _, err := m.Start(context.Background(), "group.target"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := m.Stop("group.target"); done <- err }()
	waitMember := func(want string) {
		t.Helper()
		select {
		case got := <-launch.entered:
			if got != want {
				t.Fatalf("stop order=%s want=%s", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("ordered stop did not enter")
		}
	}
	waitMember("a.service")
	clock.Advance(600 * time.Millisecond)
	releaseA()
	waitMember("b.service")
	clock.Advance(400 * time.Millisecond)
	op := waitOperationErrorCompleted(t, m, waitErr(t, done))
	if op.State != "failed" || !strings.Contains(op.CancellationReason, "deadline") {
		t.Fatal("second member received a fresh operation budget")
	}
	releaseB()
	if _, err := m.Stop("group.target"); err != nil {
		t.Fatal("stop retry failed", err)
	}
}

type dependencyOperationLauncher struct {
	fakeLauncher
	entered chan struct{}
	release chan struct{}
}

func (l *dependencyOperationLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	if spec.Unit == "slow.service" {
		close(l.entered)
		<-l.release
		return l.fakeLauncher.Start(context.Background(), spec)
	}
	return l.fakeLauncher.Start(ctx, spec)
}

func TestOperationDeadlinePreservesCompletedDependency(t *testing.T) {
	launch := &dependencyOperationLauncher{entered: make(chan struct{}), release: make(chan struct{})}
	m, clock := managerWithFake(t, launch, map[string]string{
		"ready.service": "[Service]\nExecStart=C:\\Tools\\ready.exe\n",
		"slow.service":  "[Unit]\nRequires=ready.service\nAfter=ready.service\n[Service]\nExecStart=C:\\Tools\\slow.exe\n",
	})
	m.cfg.OperationTimeout = time.Second
	var once sync.Once
	release := func() { once.Do(func() { close(launch.release) }) }
	t.Cleanup(release)
	done := make(chan error, 1)
	go func() { _, err := m.Start(context.Background(), "slow"); done <- err }()
	select {
	case <-launch.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("dependent did not enter")
	}
	assertState(t, m, "ready.service", core.Active)
	clock.Advance(time.Second)
	err := waitErr(t, done)
	release()
	waitOperationErrorCompleted(t, m, err)
	assertState(t, m, "ready.service", core.Active)
	m.mu.Lock()
	ready, slow := m.units["ready.service"].proc, m.units["slow.service"].proc
	m.mu.Unlock()
	if ready == nil || !ready.Alive() || slow != nil {
		t.Fatal("deadline undid a completed dependency or retained late work")
	}
}

func TestDefaultOperationBudgetPreservesConfiguredTimeouts(t *testing.T) {
	m := managerWith(t, &fakeLauncher{}, map[string]string{
		"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\nTimeoutStartSec=10min\nTimeoutStopSec=2min\n",
	})
	m.mu.Lock()
	defer m.mu.Unlock()
	start, err := m.graph.PlanStart("work.service")
	if err != nil {
		t.Fatal(err)
	}
	stop, err := m.graph.PlanStop("work.service")
	if err != nil {
		t.Fatal(err)
	}
	if got := m.operationTimeoutLocked(start, stop); got < 18*time.Minute {
		t.Fatalf("aggregate budget %s shortens configured phases", got)
	}
}

type secondOperationLauncher struct {
	fakeLauncher
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
	first   *blockedStopProcess
}

func (l *secondOperationLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	call := l.calls.Add(1)
	if call == 2 {
		close(l.entered)
		<-l.release
		ctx = context.Background()
	}
	p, err := l.fakeLauncher.Start(ctx, spec)
	if err == nil && call == 1 {
		l.first = &blockedStopProcess{Process: p, release: make(chan struct{})}
		return l.first, nil
	}
	return p, err
}

func TestOperationDeadlineUsesLaunchGenerationAfterQueuedStop(t *testing.T) {
	launch := &secondOperationLauncher{entered: make(chan struct{}), release: make(chan struct{})}
	m, clock := managerWithFake(t, launch, map[string]string{"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n"})
	m.cfg.OperationTimeout = time.Second
	var once sync.Once
	release := func() { once.Do(func() { close(launch.release) }) }
	t.Cleanup(release)
	if _, err := m.Start(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	var gate sync.Once
	openGate := func() { gate.Do(func() { close(launch.first.release) }) }
	defer openGate()
	stopped := make(chan error, 1)
	go func() { _, err := m.Stop("work"); stopped <- err }()
	// Admission marks stopping before its worker acquires the unit gate.
	// Wait for the actual stop call so the new start cannot overtake it.
	waitCond(t, func() bool { return launch.first.calls.Load() == 1 })
	started := make(chan error, 1)
	go func() { _, err := m.Start(context.Background(), "work"); started <- err }()
	waitCond(t, func() bool { m.ops.mu.Lock(); defer m.ops.mu.Unlock(); return m.ops.by["work.service"].refs == 2 })
	openGate()
	if err := waitErr(t, stopped); err != nil {
		t.Fatal(err)
	}
	select {
	case <-launch.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("queued launch did not enter")
	}
	clock.Advance(time.Second)
	err := waitErr(t, started)
	release()
	waitOperationErrorCompleted(t, m, err)
	m.mu.Lock()
	proc, uncertain := m.units["work.service"].proc, m.units["work.service"].stopUncertain
	m.mu.Unlock()
	if proc != nil || uncertain {
		t.Fatal("changed generation escaped operation deadline cleanup")
	}
}

type lateOperationSCM struct {
	runtime.SCM
	entered chan struct{}
	release chan struct{}
}

func (s *lateOperationSCM) Start(_ context.Context, name string, timeout time.Duration) (runtime.SCMStatus, error) {
	close(s.entered)
	<-s.release
	return s.SCM.Start(context.Background(), name, timeout)
}

type lateOperationTask struct {
	runtime.TaskScheduler
	entered chan struct{}
	release chan struct{}
}

func (s *lateOperationTask) Start(_ context.Context, name string, timeout time.Duration) (runtime.TaskStatus, error) {
	close(s.entered)
	<-s.release
	return s.TaskScheduler.Start(context.Background(), name, timeout)
}

func TestOperationDeadlineCleansLateNativeStart(t *testing.T) {
	for _, kind := range []string{"scm", "scheduled-task"} {
		t.Run(kind, func(t *testing.T) {
			clock := timers.NewFake(time.Time{})
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			cfg := Config{Clock: clock.Clock(), Launch: &fakeLauncher{}, OperationTimeout: time.Second}
			body := "[Service]\nType=" + kind + "\n"
			var inactive func() bool
			if kind == "scm" {
				backend := newFakeSCM("example-worker")
				cfg.SCM = &lateOperationSCM{SCM: backend, entered: entered, release: release}
				body += "ServiceName=example-worker\n"
				inactive = func() bool {
					st, err := backend.Query("example-worker")
					return err == nil && st.ActiveState() == "inactive"
				}
			} else {
				backend := newFakeTasks("example-worker")
				cfg.Tasks = &lateOperationTask{TaskScheduler: backend, entered: entered, release: release}
				body += "TaskName=example-worker\n"
				inactive = func() bool {
					st, err := backend.Query("example-worker")
					return err == nil && st.ActiveState() == "inactive"
				}
			}
			m := managerWithPathCfg(t, cfg, map[string]string{
				"work.service": body,
				"work.timer":   "[Timer]\nOnUnitActiveSec=7s\n",
			})
			if _, err := m.Start(context.Background(), "work.timer"); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, err := m.Start(context.Background(), "work"); done <- err }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("native start did not enter")
			}
			clock.Advance(time.Second)
			err := waitErr(t, done)
			unblock()
			op := waitOperationErrorCompleted(t, m, err)
			if op.State != "failed" || !inactive() {
				t.Fatal("late native activation survived deadline cleanup")
			}
			m.mu.Lock()
			uncertain := m.units["work.service"].stopUncertain
			m.mu.Unlock()
			if uncertain {
				t.Fatal("successful native cleanup retained uncertainty")
			}
			if !m.engine.Status("work.timer").Next.IsZero() {
				t.Fatal("canceled native start scheduled a new timer activation")
			}
		})
	}
}

func TestOperationDeadlineRejectsLateWatchOpen(t *testing.T) {
	clock := timers.NewFake(time.Time{})
	hub := newFakePathHub()
	opened := make(chan *fakePathWatch, 1)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	launch := &fakeLauncher{}
	m := managerWithPathCfg(t, Config{Clock: clock.Clock(), Launch: launch, OperationTimeout: time.Second, PathOpen: func(spec pathwatch.Spec) (pathwatch.Watch, error) {
		w, err := hub.Open(spec)
		if err != nil {
			return nil, err
		}
		opened <- w.(*fakePathWatch)
		<-release
		return w, nil
	}}, map[string]string{"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n", "work.path": "[Path]\nPathChanged=C:\\Data\\incoming\n"})
	done := make(chan error, 1)
	go func() { _, err := m.Start(context.Background(), "work.path"); done <- err }()
	var watch *fakePathWatch
	select {
	case watch = <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not open")
	}
	clock.Advance(time.Second)
	err := waitErr(t, done)
	unblock()
	waitOperationErrorCompleted(t, m, err)
	watch.mu.Lock()
	closed := watch.closed
	watch.mu.Unlock()
	if !closed || len(launch.units()) != 0 {
		t.Fatal("late watch leaked or activated companion")
	}
}
