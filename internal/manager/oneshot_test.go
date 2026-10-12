package manager

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/unit"
)

const oneshotBody = "[Service]\nType=oneshot\nExecStart=C:\\Tools\\work.exe\nTimeoutStartSec=1h\n"

func TestRepeatableOneshotCompletionAndRetainedState(t *testing.T) {
	for _, directive := range []string{"", "RemainAfterExit=no\n", "RemainAfterExit=yes\n"} {
		t.Run(directive, func(t *testing.T) {
			launch := &fakeLauncher{stdout: "completed work\n"}
			m := managerWith(t, launch, map[string]string{"work.service": oneshotBody + directive})
			retained := directive == "RemainAfterExit=yes\n"
			want := "inactive"
			if retained {
				want = "active"
			}
			var previous string
			for i := 0; i < 2; i++ {
				result, err := m.Start(context.Background(), "work")
				if err != nil || result.ActiveState != want {
					t.Fatalf("start = %+v, %v", result, err)
				}
				op, err := m.Operation(result.OperationID)
				if err != nil || op.State != "succeeded" {
					t.Fatalf("operation = %+v, %v", op, err)
				}
				st, _ := m.Status("work")
				if st.Unit.MainPID != 0 || st.Unit.TerminationUncertain || st.Unit.Error != "" {
					t.Fatalf("status = %+v", st.Unit)
				}
				if i > 0 && (st.Unit.InvocationID == previous) != retained {
					t.Fatal("incorrect invocation reuse")
				}
				previous = st.Unit.InvocationID
			}
			wantStarts := 2
			if retained {
				wantStarts = 1
			}
			if len(launch.specs()) != wantStarts || len(launch.stopped()) != wantStarts {
				t.Fatal("completion did not clean exactly one process per invocation")
			}
			logs, err := m.Logs(protocol.LogsParams{Unit: "work.service"})
			if err != nil || len(logs.Entries) != wantStarts {
				t.Fatalf("logs = %+v, %v", logs, err)
			}
			if _, err := m.Restart(context.Background(), "work"); err != nil {
				t.Fatal(err)
			}
			if len(launch.specs()) != wantStarts+1 {
				t.Fatal("restart did not execute a fresh invocation")
			}
		})
	}
}

type heldOneshotLauncher struct {
	fakeLauncher
	created chan *fakeProc
	// The first Stop of a launched oneshot is the interrupted start's
	// teardown; a later one belongs to an exit or failed-state cleanup worker,
	// which owns the unit gate and keeps workload cleanup unconfirmed until it
	// returns. stopGate holds such later Stops until closed; cleanupHeld
	// receives when a worker is held. While failCleanup is set, later Stops
	// fail instead, leaving cleanup pending with the gate released;
	// cleanupFailed receives after such a failure.
	stopGate      chan struct{}
	cleanupHeld   chan struct{}
	failCleanup   *atomic.Bool
	cleanupFailed chan struct{}
}

type gatedStopProcess struct {
	*fakeProc
	l     *heldOneshotLauncher
	stops atomic.Int32
}

func (p *gatedStopProcess) Stop(timeout time.Duration) error {
	if p.stops.Add(1) > 1 {
		if p.l.failCleanup != nil && p.l.failCleanup.Load() {
			select {
			case p.l.cleanupFailed <- struct{}{}:
			default:
			}
			return errors.New("injected cleanup failure")
		}
		if p.l.stopGate != nil {
			select {
			case p.l.cleanupHeld <- struct{}{}:
			default:
			}
			<-p.l.stopGate
		}
	}
	return p.fakeProc.Stop(timeout)
}

func (l *heldOneshotLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	if spec.Type != unit.TypeOneshot {
		return l.fakeLauncher.Start(ctx, spec)
	}
	spec.Type = unit.TypeSimple
	p, err := l.fakeLauncher.Start(ctx, spec)
	if err == nil {
		l.created <- p.(*fakeProc)
		if l.stopGate != nil || l.failCleanup != nil {
			p = &gatedStopProcess{fakeProc: p.(*fakeProc), l: l}
		}
	}
	return p, err
}

type oneshotReply struct {
	result *protocol.UnitResult
	err    error
}

func startOneshot(m *Manager, ctx context.Context, name string) <-chan oneshotReply {
	done := make(chan oneshotReply, 1)
	go func() { r, err := m.Start(ctx, name); done <- oneshotReply{r, err} }()
	return done
}

func awaitOneshotReply(t *testing.T, done <-chan oneshotReply) oneshotReply {
	t.Helper()
	select {
	case r := <-done:
		return r
	case <-time.After(3 * time.Second):
		t.Fatal("start did not complete")
		return oneshotReply{}
	}
}

func awaitOneshotProcess(t *testing.T, l *heldOneshotLauncher) *fakeProc {
	t.Helper()
	select {
	case p := <-l.created:
		return p
	case <-time.After(3 * time.Second):
		t.Fatal("oneshot did not launch")
		return nil
	}
}

func TestRepeatableOneshotCoalescesConcurrentStarts(t *testing.T) {
	l := &heldOneshotLauncher{created: make(chan *fakeProc, 4)}
	m := managerWith(t, l, map[string]string{"work.service": oneshotBody})
	first := startOneshot(m, context.Background(), "work")
	p := awaitOneshotProcess(t, l)
	assertState(t, m, "work.service", core.Activating)
	ctx := &observedWaitContext{Context: context.Background(), entered: make(chan struct{})}
	second := startOneshot(m, ctx, "work")
	select {
	case <-ctx.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("second start did not join")
	}
	p.die(0)
	a, b := awaitOneshotReply(t, first), awaitOneshotReply(t, second)
	if a.err != nil || b.err != nil || a.result.OperationID != b.result.OperationID || len(l.specs()) != 1 {
		t.Fatalf("overlap: %+v %+v", a, b)
	}
	third := startOneshot(m, context.Background(), "work")
	awaitOneshotProcess(t, l).die(0)
	if r := awaitOneshotReply(t, third); r.err != nil || len(l.specs()) != 2 {
		t.Fatalf("repeat: %+v", r)
	}
}

func TestRepeatableOneshotDependencyCompletion(t *testing.T) {
	for _, code := range []uint32{0, 3} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			l := &heldOneshotLauncher{created: make(chan *fakeProc, 2)}
			m := managerWith(t, l, map[string]string{
				"work.service":     oneshotBody,
				"consumer.service": "[Unit]\nRequires=work.service\nAfter=work.service\n[Service]\nExecStart=C:\\Tools\\consumer.exe\n",
			})
			done := startOneshot(m, context.Background(), "consumer")
			p := awaitOneshotProcess(t, l)
			if len(l.specs()) != 1 {
				t.Fatal("dependent ran before prerequisite completion")
			}
			p.die(code)
			r := awaitOneshotReply(t, done)
			if code == 0 {
				if r.err != nil || len(l.specs()) != 2 {
					t.Fatalf("successful inactive prerequisite blocked dependent: %+v", r)
				}
				assertState(t, m, "work.service", core.Inactive)
				assertState(t, m, "consumer.service", core.Active)
			} else {
				if r.err == nil || len(l.specs()) != 1 {
					t.Fatal("failed prerequisite did not block dependent")
				}
				assertState(t, m, "work.service", core.Failed)
			}
		})
	}
}

func TestRepeatableOneshotSharedPrerequisiteDoesNotQueueRerun(t *testing.T) {
	for _, code := range []uint32{0, 3} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			l := &heldOneshotLauncher{created: make(chan *fakeProc, 3)}
			m := managerWith(t, l, map[string]string{
				"work.service": oneshotBody,
				"a.target":     "[Unit]\nRequires=work.service\nAfter=work.service\n",
				"b.target":     "[Unit]\nRequires=work.service\nAfter=work.service\n",
			})
			a := startOneshot(m, context.Background(), "a.target")
			p := awaitOneshotProcess(t, l)
			b := startOneshot(m, context.Background(), "b.target")
			waitCond(t, func() bool { st, _ := m.Status("b.target"); return st.Unit.LastOperationID != "" })
			p.die(code)
			ar, br := awaitOneshotReply(t, a), awaitOneshotReply(t, b)
			if (ar.err == nil) != (code == 0) || (br.err == nil) != (code == 0) || len(l.specs()) != 1 {
				t.Fatalf("shared execution: %+v %+v, starts=%d", ar, br, len(l.specs()))
			}
		})
	}
}

func TestRepeatableOneshotReloadCapturesCompletionPolicy(t *testing.T) {
	for _, retain := range []bool{false, true} {
		t.Run(fmt.Sprint(retain), func(t *testing.T) {
			l := &heldOneshotLauncher{created: make(chan *fakeProc, 3)}
			body := func(value bool) string { return oneshotBody + fmt.Sprintf("RemainAfterExit=%t\n", value) }
			m := managerWith(t, l, map[string]string{"work.service": body(retain)})
			done := startOneshot(m, context.Background(), "work")
			p := awaitOneshotProcess(t, l)
			writeUnit(t, m.cfg.UnitsDir(), "work.service", body(!retain))
			if _, err := m.Reload(); err != nil {
				t.Fatal(err)
			}
			p.die(0)
			r := awaitOneshotReply(t, done)
			want := "inactive"
			if retain {
				want = "active"
			}
			if r.err != nil || r.result.ActiveState != want {
				t.Fatalf("reload retargeted invocation: %+v", r)
			}
			if _, err := m.Stop("work"); err != nil {
				t.Fatal(err)
			}
			done = startOneshot(m, context.Background(), "work")
			awaitOneshotProcess(t, l).die(0)
			r = awaitOneshotReply(t, done)
			want = "active"
			if retain {
				want = "inactive"
			}
			if r.err != nil || r.result.ActiveState != want {
				t.Fatalf("new invocation ignored reload: %+v", r)
			}
		})
	}
}

func TestRepeatableOneshotInterruptedWait(t *testing.T) {
	for _, action := range []string{"stop", "cancel", "timeout", "timeout-refused", "restart"} {
		t.Run(action, func(t *testing.T) {
			l := &heldOneshotLauncher{created: make(chan *fakeProc, 3)}
			switch action {
			case "timeout":
				l.stopGate, l.cleanupHeld = make(chan struct{}), make(chan struct{}, 1)
			case "timeout-refused":
				l.failCleanup, l.cleanupFailed = &atomic.Bool{}, make(chan struct{}, 1)
				l.failCleanup.Store(true)
			}
			m, clock := managerWithFake(t, l, map[string]string{"work.service": oneshotBody})
			// Registered after managerWithFake, so it runs before the manager's
			// teardown on every exit, including a failed assertion while a
			// cleanup worker is held.
			releaseCleanup := sync.OnceFunc(func() {
				if l.stopGate != nil {
					close(l.stopGate)
				}
				if l.failCleanup != nil {
					l.failCleanup.Store(false)
				}
			})
			t.Cleanup(releaseCleanup)
			done := startOneshot(m, context.Background(), "work")
			p := awaitOneshotProcess(t, l)
			waitCond(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.units["work.service"].startCancel != nil })
			var restarted chan oneshotReply
			switch action {
			case "stop":
				if _, err := m.Stop("work"); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				st, _ := m.Status("work")
				if _, err := m.CancelOperation(context.Background(), st.Unit.LastOperationID); err != nil {
					t.Fatal(err)
				}
			case "timeout", "timeout-refused":
				clock.Advance(time.Hour)
			case "restart":
				restarted = make(chan oneshotReply, 1)
				go func() { r, err := m.Restart(context.Background(), "work"); restarted <- oneshotReply{r, err} }()
			}
			r := awaitOneshotReply(t, done)
			if r.err == nil {
				t.Fatal("interrupted start succeeded")
			}
			interrupted := waitOperationErrorCompleted(t, m, r.err)
			// A timed-out start publishes its failure before workload cleanup
			// is confirmed (#261); a fresh start must not launch before then.
			var waiting <-chan oneshotReply
			switch action {
			case "timeout":
				waiting = startBehindHeldCleanup(t, m, l, interrupted.ID, releaseCleanup)
			case "timeout-refused":
				refuseStartWhileCleanupFailed(t, m, l, releaseCleanup)
			}
			if p.Alive() {
				t.Fatal("interrupted invocation survived")
			}
			if restarted != nil {
				awaitOneshotProcess(t, l).die(0)
				if r := awaitOneshotReply(t, restarted); r.err != nil || r.result.ActiveState != "inactive" {
					t.Fatalf("restart: %+v", r)
				}
			}
			// A completed stop/failure must not prevent an explicit fresh run;
			// the start that waited behind the held cleanup is that run.
			done = waiting
			if done == nil {
				done = startOneshot(m, context.Background(), "work")
			}
			awaitOneshotProcess(t, l).die(0)
			if r := awaitOneshotReply(t, done); r.err != nil || r.result.ActiveState != "inactive" {
				t.Fatalf("retry: %+v", r)
			}
		})
	}
}

func assertWorkloadCleanupPending(t *testing.T, m *Manager) {
	t.Helper()
	st, err := m.Status("work")
	if err != nil || !st.Unit.TerminationUncertain || !containsEnv(st.Unit.PendingCleanup, "workload") {
		t.Fatalf("timed-out workload cleanup not pending: %+v %v", st, err)
	}
}

func waitWorkloadCleanupConfirmed(t *testing.T, m *Manager) {
	t.Helper()
	waitCond(t, func() bool {
		st, err := m.Status("work")
		return err == nil && !st.Unit.TerminationUncertain && len(st.Unit.PendingCleanup) == 0
	})
}

// A cleanup worker held inside Stop owns the unit gate. A start issued then is
// admitted as an operation but cannot reach launch admission; once the worker
// confirms cleanup it launches. Returns that start's reply channel.
func startBehindHeldCleanup(t *testing.T, m *Manager, l *heldOneshotLauncher, interrupted string, release func()) <-chan oneshotReply {
	t.Helper()
	select {
	case <-l.cleanupHeld:
	case <-time.After(3 * time.Second):
		t.Fatal("cleanup worker did not reach Stop")
	}
	assertWorkloadCleanupPending(t, m)
	done := startOneshot(m, context.Background(), "work")
	var op *protocol.OperationResult
	waitCond(t, func() bool {
		st, err := m.Status("work")
		if err != nil || st.Unit.LastOperationID == interrupted {
			return false
		}
		op, err = m.Operation(st.Unit.LastOperationID)
		return err == nil
	})
	select {
	case r := <-done:
		t.Fatalf("start finished while cleanup was held: %+v", r)
	default:
	}
	if op.State != "running" || op.Action != "start" || len(l.created) != 0 {
		t.Fatalf("start behind held cleanup: %+v, launched %d", op, len(l.created))
	}
	release()
	waitWorkloadCleanupConfirmed(t, m)
	return done
}

// A failed cleanup worker leaves workload cleanup pending and releases the
// unit gate, so a start reaches launch admission and is refused there without
// launching. A retried stop then confirms cleanup.
func refuseStartWhileCleanupFailed(t *testing.T, m *Manager, l *heldOneshotLauncher, release func()) {
	t.Helper()
	select {
	case <-l.cleanupFailed:
	case <-time.After(3 * time.Second):
		t.Fatal("cleanup worker did not fail")
	}
	assertWorkloadCleanupPending(t, m)
	if r, err := m.Start(context.Background(), "work"); err == nil || !strings.Contains(err.Error(), "termination is unconfirmed") {
		t.Fatalf("start with unconfirmed cleanup: %+v %v", r, err)
	}
	if len(l.created) != 0 {
		t.Fatal("refused start launched a process")
	}
	assertWorkloadCleanupPending(t, m)
	release()
	if _, err := m.Stop("work"); err != nil {
		t.Fatalf("retry stop: %v", err)
	}
	waitWorkloadCleanupConfirmed(t, m)
}

func TestRepeatableOneshotCleanupFailureRetainsOwnership(t *testing.T) {
	l := &failedStopLauncher{}
	m := managerWith(t, l, map[string]string{"work.service": oneshotBody})
	if _, err := m.Start(context.Background(), "work"); err == nil {
		t.Fatal("cleanup failure reported success")
	}
	p := l.proc
	t.Cleanup(func() { p.fail.Store(false); _ = p.Process.Stop(time.Second) })
	st, _ := m.Status("work")
	if st.Unit.ActiveState != "failed" || !st.Unit.TerminationUncertain {
		t.Fatalf("status = %+v", st.Unit)
	}
	if _, err := m.Start(context.Background(), "work"); err == nil || len(l.specs()) != 1 {
		t.Fatal("uncertain cleanup allowed a fresh run")
	}
	p.fail.Store(false)
	if _, err := m.Stop("work"); err != nil {
		t.Fatal(err)
	}
	st, _ = m.Status("work")
	if st.Unit.ActiveState != "inactive" || st.Unit.TerminationUncertain {
		t.Fatalf("stop retry = %+v", st.Unit)
	}
}

type recoveryCleanupLauncher struct {
	fakeLauncher
	failed chan *failedStopProcess
}

func (l *recoveryCleanupLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	p, err := l.fakeLauncher.Start(ctx, spec)
	if err != nil || len(l.specs()) == 1 {
		return p, err
	}
	failure := &failedStopProcess{Process: p}
	failure.fail.Store(true)
	l.failed <- failure
	return failure, nil
}

func TestRepeatableOneshotRecoveryCleanupFailureIsFailed(t *testing.T) {
	l := &recoveryCleanupLauncher{failed: make(chan *failedStopProcess, 1)}
	m, clock := managerWithFake(t, l, map[string]string{"work.service": oneshotBody + "Restart=always\nRestartSec=5s\n"})
	if _, err := m.Start(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	waitCond(t, func() bool { return clock.WaitingAt(5 * time.Second) })
	clock.Advance(5 * time.Second)
	var p *failedStopProcess
	select {
	case p = <-l.failed:
	case <-time.After(3 * time.Second):
		t.Fatal("recovery did not launch")
	}
	t.Cleanup(func() { p.fail.Store(false); _ = p.Process.Stop(time.Second) })
	waitCond(t, func() bool {
		st, _ := m.Status("work")
		return st.Unit.ActiveState == "failed" && st.Unit.TerminationUncertain && st.Unit.Error != ""
	})
	p.fail.Store(false)
	if _, err := m.Stop("work"); err != nil {
		t.Fatal(err)
	}
}
