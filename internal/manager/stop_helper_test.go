package manager

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/unit"
)

type cooperativeLauncher struct {
	fakeLauncher
	guard      sync.Mutex
	main       *fakeProc
	helper     runtime.Process
	helperSpec runtime.StartSpec
	mode       string
	entered    chan struct{}
	release    chan struct{}
	output     *io.PipeWriter
}

func (l *cooperativeLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	if !strings.Contains(spec.Argv[0], "stop") {
		if l.mode == "failed-start" {
			return nil, errors.New("injected start failure")
		}
		p, err := l.fakeLauncher.Start(ctx, spec)
		if err == nil {
			l.guard.Lock()
			l.main = p.(*fakeProc)
			l.guard.Unlock()
		}
		return p, err
	}
	l.guard.Lock()
	l.helperSpec = spec
	main := l.main
	l.guard.Unlock()
	if l.entered != nil {
		close(l.entered)
	}
	if l.mode == "late" {
		<-l.release
		ctx = context.Background()
	}
	if l.mode == "cooperate" || l.mode == "failed-cleanup" || l.mode == "hung-output" {
		main.die(0)
	}
	if l.mode == "hang" {
		spec.Type = unit.TypeSimple
	}
	p, err := l.fakeLauncher.Start(ctx, spec)
	if err != nil {
		return p, err
	}
	p.(*fakeProc).stdout = io.NopCloser(strings.NewReader("stop helper output\n"))
	if l.mode == "hung-output" {
		reader, writer := io.Pipe()
		// Model a reader whose underlying completion outlives process cleanup.
		p.(*fakeProc).stdout = io.NopCloser(reader)
		l.guard.Lock()
		l.output = writer
		l.guard.Unlock()
	}
	if l.mode == "failed-cleanup" {
		failed := &failedStopProcess{Process: p}
		failed.fail.Store(true)
		p = failed
	}
	l.guard.Lock()
	l.helper = p
	l.guard.Unlock()
	return p, nil
}

const cooperativeService = "[Service]\nExecStart=C:\\Tools\\work.exe\nExecStop=C:\\Tools\\stop.exe\nWorkingDirectory=C:\\Tools\nEnvironment=CONFIG=captured\nTimeoutStopSec=5s\n"

func TestExecStopRetainsUnfinishedOutputUntilRetry(t *testing.T) {
	l := &cooperativeLauncher{mode: "hung-output"}
	m := managerWith(t, l, map[string]string{"work.service": strings.ReplaceAll(cooperativeService, "TimeoutStopSec=5s", "TimeoutStopSec=200ms")})
	defer func() {
		l.guard.Lock()
		defer l.guard.Unlock()
		if l.output != nil {
			_ = l.output.Close()
		}
	}()
	if _, err := m.Start(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stop("work"); err == nil {
		t.Fatal("unfinished helper output reported complete")
	}
	m.mu.Lock()
	h := m.units["work.service"].stopHelper
	m.mu.Unlock()
	if h == nil {
		t.Fatal("helper output lost its owner")
	}
	select {
	case <-h.done:
	case <-time.After(time.Second):
		t.Fatal("helper cleanup response did not expire")
	}
	if _, err := m.Start(context.Background(), "work"); err == nil {
		t.Fatal("replacement admitted with unfinished helper output")
	}
	l.guard.Lock()
	_ = l.output.Close()
	l.guard.Unlock()
	if _, err := m.Stop("work"); err != nil {
		t.Fatal("retry failed to join helper output", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.stopHelpers) != 0 || m.units["work.service"].cleanupPending() {
		t.Fatal("joined output still owns cleanup capacity")
	}
	if len(l.specs()) != 2 {
		t.Fatal("output retry relaunched the helper")
	}
}

func TestExecStopUsesCapturedDefinitionAndDoesNotRepeat(t *testing.T) {
	l := &cooperativeLauncher{mode: "cooperate"}
	m := managerWith(t, l, map[string]string{"work.service": cooperativeService})
	if _, err := m.Start(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, m.cfg.UnitsDir(), "work.service", strings.ReplaceAll(strings.ReplaceAll(cooperativeService, "stop.exe", "stop-new.exe"), "CONFIG=captured", "CONFIG=new"))
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stop("work"); err != nil {
		t.Fatal(err)
	}
	l.guard.Lock()
	spec, helper := l.helperSpec, l.helper
	l.guard.Unlock()
	if spec.Argv[0] != `C:\Tools\stop.exe` || spec.Dir != `C:\Tools` || !containsEnv(spec.Env, "CONFIG=captured") || !containsEnv(spec.Env, "MAINPID=1") {
		t.Fatalf("helper lost captured context: %+v", spec)
	}
	if helper == nil || helper.Alive() {
		t.Fatal("helper survived successful stop")
	}
	if _, err := m.Stop("work"); err != nil {
		t.Fatal(err)
	}
	if len(l.specs()) != 2 {
		t.Fatal("stop retry reran cooperative command")
	}
	entries, err := m.journal.Read("work.service")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		found = found || entry.Message == "stop helper output" && strings.HasSuffix(entry.InvocationID, "-stop")
	}
	if !found {
		t.Fatal("stop helper output missing from unit journal")
	}
}

func containsEnv(env []string, want string) bool {
	for _, entry := range env {
		if entry == want {
			return true
		}
	}
	return false
}

func TestExecStopRestartRunsOnceForEachReplacedInvocation(t *testing.T) {
	l := &cooperativeLauncher{mode: "cooperate"}
	m := managerWith(t, l, map[string]string{"work.service": cooperativeService})
	if _, err := m.Start(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := m.Restart(context.Background(), "work"); err != nil {
			t.Fatal(err)
		}
		if got := len(l.specs()); got != 3+2*i {
			t.Fatalf("restart omitted or repeated helper: %d launches", got)
		}
	}
	if _, err := m.Stop("work"); err != nil {
		t.Fatal(err)
	}
	if len(l.specs()) != 6 {
		t.Fatal("replacement invocation did not receive its own stop helper")
	}
}

func TestExecStopLateLaunchCannotDelayForcedWorkloadCleanup(t *testing.T) {
	l := &cooperativeLauncher{mode: "late", entered: make(chan struct{}), release: make(chan struct{})}
	m, clock := managerWithFake(t, l, map[string]string{"work.service": cooperativeService})
	m.cfg.OperationTimeout = time.Second
	var once sync.Once
	unblock := func() { once.Do(func() { close(l.release) }) }
	defer unblock()
	if _, err := m.Start(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { _, err := m.Stop("work"); stopped <- err }()
	select {
	case <-l.entered:
	case <-time.After(time.Second):
		t.Fatal("helper launch did not enter")
	}
	clock.Advance(500 * time.Millisecond)
	if err := waitErr(t, stopped); err == nil {
		t.Fatal("late helper reported successful stop")
	}
	l.guard.Lock()
	main := l.main
	l.guard.Unlock()
	if main.Alive() {
		t.Fatal("helper launch stalled forced main-process cleanup")
	}
	st, err := m.Status("work")
	if err != nil || !st.Unit.TerminationUncertain || !containsEnv(st.Unit.PendingCleanup, "stop-helper") {
		t.Fatalf("late launch ownership absent: %+v %v", st, err)
	}
	if _, err := m.Start(context.Background(), "work"); err == nil {
		t.Fatal("replacement admitted while helper creation remained owned")
	}
	unblock()
	waitCond(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return len(m.stopHelpers) == 0 })
	l.guard.Lock()
	helper := l.helper
	l.guard.Unlock()
	if helper == nil || helper.Alive() {
		t.Fatal("late helper was not adopted and terminated")
	}
	if _, err := m.Stop("work"); err != nil {
		t.Fatal(err)
	}
	if len(l.specs()) != 2 {
		t.Fatal("retry launched another helper")
	}
}

func TestExecStopHungHelperForcesBothJobs(t *testing.T) {
	l := &cooperativeLauncher{mode: "hang", entered: make(chan struct{})}
	m, clock := managerWithFake(t, l, map[string]string{"work.service": cooperativeService})
	if _, err := m.Start(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { _, err := m.Stop("work"); stopped <- err }()
	waitCond(t, func() bool { l.guard.Lock(); defer l.guard.Unlock(); return l.helper != nil })
	clock.Advance(4 * time.Second)
	if err := waitErr(t, stopped); err == nil {
		t.Fatal("hung helper did not fail cooperative stop")
	}
	waitCond(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return len(m.stopHelpers) == 0 })
	l.guard.Lock()
	main, helper := l.main, l.helper
	l.guard.Unlock()
	if main.Alive() || helper.Alive() {
		t.Fatal("forced fallback left a job alive")
	}
}

type blockedHelperLauncher struct {
	calls   atomic.Int32
	release <-chan struct{}
}

func (l *blockedHelperLauncher) Start(context.Context, runtime.StartSpec) (runtime.Process, error) {
	l.calls.Add(1)
	<-l.release
	return nil, errors.New("injected late launch failure")
}

func TestStopHelperCapacityAndCloseRetainAcceptedLaunches(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	l := &blockedHelperLauncher{release: release}
	m := &Manager{units: make(map[string]*unitRuntime), launch: l}
	for i := 0; i < maxStopHelpers+1; i++ {
		name := fmt.Sprintf("work-%d.service", i)
		u := &unit.Unit{Name: name, Kind: unit.KindService, Service: &unit.ServiceSpec{ExecStop: []string{`C:\Tools\stop.exe`}}}
		rt := &unitRuntime{unit: u, state: core.Active, invocation: fmt.Sprintf("inv-%d", i)}
		m.mu.Lock()
		m.units[name] = rt
		m.mu.Unlock()
		h, _, err := m.acceptStopHelper(context.Background(), runtimeIdentity{name: name, record: rt}, u, nil, true, time.Second)
		if i < maxStopHelpers && (h == nil || err != nil) {
			t.Fatal("reserved helper admission", err)
		}
		if i == maxStopHelpers && (h != nil || err == nil) {
			t.Fatal("helper capacity was exceeded")
		}
	}
	waitCond(t, func() bool { return l.calls.Load() == maxStopHelpers })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := m.CloseContext(ctx); err == nil {
		t.Fatal("Close abandoned accepted helper launches")
	}
	m.mu.Lock()
	retained := len(m.stopHelpers)
	m.mu.Unlock()
	if retained != maxStopHelpers {
		t.Fatal("deadline released native helper ownership")
	}
	unblock()
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.CloseContext(ctx); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.stopHelpers) != 0 {
		t.Fatal("completed helpers retained admission")
	}
}

func TestExecStopCleanupFailureRetainsOnlyHelper(t *testing.T) {
	l := &cooperativeLauncher{mode: "failed-cleanup"}
	m := managerWith(t, l, map[string]string{"work.service": cooperativeService})
	if _, err := m.Start(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stop("work"); err == nil {
		t.Fatal("failed helper cleanup reported success")
	}
	l.guard.Lock()
	helper := l.helper.(*failedStopProcess)
	l.guard.Unlock()
	defer helper.fail.Store(false)
	m.mu.Lock()
	rt := m.units["work.service"]
	retained := rt.proc == nil && rt.cleanup == cleanupHelper && rt.stopHelper != nil
	m.mu.Unlock()
	if !retained {
		t.Fatal("helper failure lost ownership or retained confirmed workload")
	}
	if _, err := m.Start(context.Background(), "work"); err == nil {
		t.Fatal("helper cleanup failure admitted replacement")
	}
	helper.fail.Store(false)
	if _, err := m.Stop("work"); err != nil {
		t.Fatal(err)
	}
	if len(l.specs()) != 2 {
		t.Fatal("cleanup retry reran helper command")
	}
}

func TestExecStopNaturalExitAndOneshotSemantics(t *testing.T) {
	for _, kind := range []string{"simple", "oneshot", "retained"} {
		t.Run(kind, func(t *testing.T) {
			l := &cooperativeLauncher{mode: "cooperate"}
			body := cooperativeService
			if kind != "simple" {
				body += "Type=oneshot\n"
			}
			if kind == "retained" {
				body += "RemainAfterExit=yes\n"
			}
			m := managerWith(t, l, map[string]string{"work.service": body})
			if _, err := m.Start(context.Background(), "work"); err != nil {
				t.Fatal(err)
			}
			if kind == "simple" {
				l.guard.Lock()
				p := l.main
				l.guard.Unlock()
				p.die(0)
			}
			if kind == "retained" {
				if len(l.specs()) != 1 {
					t.Fatal("retained oneshot stopped immediately")
				}
				if _, err := m.Stop("work"); err != nil {
					t.Fatal(err)
				}
			}
			want := core.Inactive
			if kind == "simple" {
				want = core.Failed
			}
			waitState(t, m, "work.service", want)
			waitCond(t, func() bool { return len(l.specs()) == 2 })
			if _, err := m.Stop("work"); err != nil {
				t.Fatal(err)
			}
			if len(l.specs()) != 2 {
				t.Fatal("completed invocation ran another helper")
			}
		})
	}
}

func TestExecStopIsSkippedAfterFailedStart(t *testing.T) {
	l := &cooperativeLauncher{mode: "failed-start"}
	m := managerWith(t, l, map[string]string{"work.service": cooperativeService})
	if _, err := m.Start(context.Background(), "work"); err == nil {
		t.Fatal("injected failure missing")
	}
	if _, err := m.Stop("work"); err != nil {
		t.Fatal(err)
	}
	l.guard.Lock()
	defer l.guard.Unlock()
	if l.helper != nil {
		t.Fatal("ExecStop ran after failed startup")
	}
}

func TestStopBudgetReservesParentDeadline(t *testing.T) {
	m, clock := managerWithFake(t, &fakeLauncher{}, map[string]string{})
	ctx := m.withClockBudget(context.Background(), 2*time.Second)
	clock.Advance(1500 * time.Millisecond)
	remaining := m.remainingStopBudget(ctx, 5*time.Second)
	if remaining != 500*time.Millisecond || stopForceReserve(remaining) != 250*time.Millisecond {
		t.Fatal("cooperative budget ignored accepted operation deadline")
	}
}
