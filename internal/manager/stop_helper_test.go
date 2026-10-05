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

// misleadingEnvironment configures values that the helper contract overrides.
const misleadingEnvironment = "Environment=MAINPID=4242\nEnvironment=winunit_invocation_id=configured\n"

func TestExecStopUsesCapturedDefinitionAndDoesNotRepeat(t *testing.T) {
	l := &cooperativeLauncher{mode: "cooperate"}
	body := cooperativeService + misleadingEnvironment
	m := managerWith(t, l, map[string]string{"work.service": body})
	if _, err := m.Start(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	mainID := mustInvocationID(t, m, "work")
	if got := envValues(l.specs()[0].Env, "WINUNIT_INVOCATION_ID"); len(got) != 1 || got[0] != mainID {
		t.Fatalf("main invocation environment = %v, status %s", got, mainID)
	}
	writeUnit(t, m.cfg.UnitsDir(), "work.service", strings.ReplaceAll(strings.ReplaceAll(body, "stop.exe", "stop-new.exe"), "CONFIG=captured", "CONFIG=new"))
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stop("work"); err != nil {
		t.Fatal(err)
	}
	l.guard.Lock()
	spec, helper := l.helperSpec, l.helper
	l.guard.Unlock()
	if spec.Argv[0] != `C:\Tools\stop.exe` || spec.Dir != `C:\Tools` || !containsEnv(spec.Env, "CONFIG=captured") {
		t.Fatalf("helper lost captured context: %+v", spec)
	}
	// Contract: the helper's WINUNIT_INVOCATION_ID is the captured main
	// invocation plus one final "-stop"; MAINPID is the live main process.
	// Both replace configured or inherited values, whatever their case.
	helperID := envValues(spec.Env, "WINUNIT_INVOCATION_ID")
	if len(helperID) != 1 || helperID[0] != mainID+"-stop" || strings.TrimSuffix(helperID[0], "-stop") != mainID {
		t.Fatalf("helper invocation environment = %v, main %s", helperID, mainID)
	}
	if pid := envValues(spec.Env, "MAINPID"); len(pid) != 1 || pid[0] != "1" {
		t.Fatalf("helper MAINPID = %v", pid)
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
		found = found || entry.Message == "stop helper output" && entry.InvocationID == mainID+"-stop"
	}
	if !found {
		t.Fatal("stop helper output missing from unit journal")
	}
}

// envValues returns every value of name, matching names case-insensitively as
// Windows does.
func envValues(env []string, name string) []string {
	var out []string
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, name) {
			out = append(out, value)
		}
	}
	return out
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
		replaced := mustInvocationID(t, m, "work")
		if _, err := m.Restart(context.Background(), "work"); err != nil {
			t.Fatal(err)
		}
		specs := l.specs()
		if got := len(specs); got != 3+2*i {
			t.Fatalf("restart omitted or repeated helper: %d launches", got)
		}
		current := mustInvocationID(t, m, "work")
		if helper := envValues(specs[len(specs)-2].Env, "WINUNIT_INVOCATION_ID"); current == replaced || len(helper) != 1 || helper[0] != replaced+"-stop" {
			t.Fatalf("helper for replaced %s named %v; replacement %s", replaced, helper, current)
		}
		if main := envValues(specs[len(specs)-1].Env, "WINUNIT_INVOCATION_ID"); len(main) != 1 || main[0] != current {
			t.Fatalf("replacement main environment = %v, status %s", main, current)
		}
	}
	last := mustInvocationID(t, m, "work")
	if _, err := m.Stop("work"); err != nil {
		t.Fatal(err)
	}
	specs := l.specs()
	if len(specs) != 6 {
		t.Fatal("replacement invocation did not receive its own stop helper")
	}
	if helper := envValues(specs[5].Env, "WINUNIT_INVOCATION_ID"); len(helper) != 1 || helper[0] != last+"-stop" {
		t.Fatalf("final helper named %v, want %s-stop", helper, last)
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
			body := cooperativeService + misleadingEnvironment
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
			mainID := mustInvocationID(t, m, "work")
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
			// The main process had exited when the helper started: no MAINPID,
			// not even the configured one, and the exited invocation's ID.
			helper := l.specs()[1].Env
			if pid := envValues(helper, "MAINPID"); len(pid) != 0 {
				t.Fatalf("helper after natural exit got MAINPID %v", pid)
			}
			if id := envValues(helper, "WINUNIT_INVOCATION_ID"); len(id) != 1 || id[0] != mainID+"-stop" {
				t.Fatalf("helper after natural exit named %v, want %s-stop", id, mainID)
			}
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

// The forced-cleanup reserve is min(total/2, max(1s, total/5)) of the
// remaining stop budget, not always one fifth.
func TestStopForceReserveFormula(t *testing.T) {
	for _, tc := range []struct{ total, reserve time.Duration }{
		{0, 0},
		{200 * time.Millisecond, 100 * time.Millisecond},
		{500 * time.Millisecond, 250 * time.Millisecond},
		{2 * time.Second, time.Second},
		{3 * time.Second, time.Second},
		{5 * time.Second, time.Second},
		{10 * time.Second, 2 * time.Second},
		{90 * time.Second, 18 * time.Second},
	} {
		if got := stopForceReserve(tc.total); got != tc.reserve {
			t.Errorf("reserve(%v) = %v, want %v", tc.total, got, tc.reserve)
		}
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
	// A partly consumed long operation budget: the unit's own limit binds
	// first, and the reserve follows the remaining total.
	ctx = m.withClockBudget(context.Background(), 10*time.Second)
	clock.Advance(4 * time.Second)
	if remaining := m.remainingStopBudget(ctx, 5*time.Second); remaining != 5*time.Second || stopForceReserve(remaining) != time.Second {
		t.Fatalf("unit limit within a consumed budget: %v", remaining)
	}
	clock.Advance(3 * time.Second)
	if remaining := m.remainingStopBudget(ctx, 5*time.Second); remaining != 3*time.Second || stopForceReserve(remaining) != time.Second {
		t.Fatalf("consumed budget below the unit limit: %v", remaining)
	}
}
