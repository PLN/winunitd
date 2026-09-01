package manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/timers"
	"github.com/PLN/winunitd/internal/unit"
)

func TestTypeSCMDoesNotCreateProcess(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	scm := newFakeSCM("MSSQLSERVER")
	m := managerWithSCM(t, launch, scm, map[string]string{
		"mssql.service": `
[Service]
Type=scm
ServiceName=MSSQLSERVER
`,
	})
	st, err := m.Start(context.Background(), "mssql")
	if err != nil {
		t.Fatal(err)
	}
	if st.ActiveState != "active" {
		t.Fatalf("start = %+v", st)
	}
	if specs := launch.specs(); len(specs) != 0 {
		t.Fatalf("Type=scm must not CreateProcess: %+v", specs)
	}
	if got := scm.nstarts(); got != 1 {
		t.Fatalf("SCM starts = %d", got)
	}

	status, err := m.Status("mssql")
	if err != nil {
		t.Fatal(err)
	}
	if status.Unit == nil || status.Unit.ActiveState != "active" {
		t.Fatalf("status = %+v", status.Unit)
	}
	if status.Unit.InvocationID != "" {
		t.Fatalf("Type=scm must omit InvocationID: %+v", status.Unit)
	}
	if status.Unit.MainPID != 42 {
		t.Fatalf("MainPID = %d", status.Unit.MainPID)
	}
}

func TestTypeSCMAlreadyRunningStartIsOK(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	scm := newFakeSCM("MSSQLSERVER")
	scm.set("MSSQLSERVER", runtime.SCMRunning, 7)
	m := managerWithSCM(t, launch, scm, map[string]string{
		"mssql.service": `
[Service]
Type=scm
ServiceName=MSSQLSERVER
`,
	})
	st, err := m.Start(context.Background(), "mssql")
	if err != nil {
		t.Fatal(err)
	}
	if st.ActiveState != "active" {
		t.Fatalf("start = %+v", st)
	}
	again, err := m.Start(context.Background(), "mssql")
	if err != nil {
		t.Fatal(err)
	}
	if again.ActiveState != "active" {
		t.Fatalf("second start = %+v", again)
	}
	if specs := launch.specs(); len(specs) != 0 {
		t.Fatalf("Type=scm must not CreateProcess: %+v", specs)
	}
}

func TestTypeSCMStopInactiveAndAlreadyStopped(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	scm := newFakeSCM("MSSQLSERVER")
	m := managerWithSCM(t, launch, scm, map[string]string{
		"mssql.service": `
[Service]
Type=scm
ServiceName=MSSQLSERVER
`,
	})
	if _, err := m.Start(context.Background(), "mssql"); err != nil {
		t.Fatal(err)
	}
	st, err := m.Stop("mssql")
	if err != nil {
		t.Fatal(err)
	}
	if st.ActiveState != "inactive" {
		t.Fatalf("stop = %+v", st)
	}
	status, err := m.Status("mssql")
	if err != nil {
		t.Fatal(err)
	}
	if status.Unit.ActiveState != "inactive" {
		t.Fatalf("status after stop = %+v", status.Unit)
	}
	again, err := m.Stop("mssql")
	if err != nil {
		t.Fatal(err)
	}
	if again.ActiveState != "inactive" {
		t.Fatalf("already-stopped = %+v", again)
	}
	if specs := launch.specs(); len(specs) != 0 {
		t.Fatalf("Type=scm must not CreateProcess: %+v", specs)
	}
}

func TestTypeSCMAfterWaitsUntilRunning(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	scm := newFakeSCM("MSSQLSERVER")
	scm.startDelay = 80 * time.Millisecond
	m := managerWithSCM(t, launch, scm, map[string]string{
		"mssql.service": `
[Service]
Type=scm
ServiceName=MSSQLSERVER
`,
		"app.service": `
[Unit]
Requires=mssql.service
After=mssql.service
[Service]
Type=simple
ExecStart=C:\Tools\app.exe
WorkingDirectory=C:\Tools
`,
	})
	if _, err := m.Start(context.Background(), "app"); err != nil {
		t.Fatal(err)
	}
	if specs := launch.specs(); len(specs) != 1 || specs[0].Unit != "app.service" {
		t.Fatalf("launcher specs = %+v", specs)
	}
	done := scm.lastStartDone()
	if done.IsZero() {
		t.Fatal("SCM start was not recorded")
	}
	started := launch.firstStartAt()
	if started.IsZero() || started.Before(done) {
		t.Fatalf("Type=simple started at %s before SCM reported running at %s", started, done)
	}
}

func TestUserScopeReloadRejectsTypeSCM(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, units, "mssql.service", `
[Service]
Type=scm
ServiceName=MSSQLSERVER
`)
	m, err := New(Config{BaseDir: dir, Launch: launch, SCM: newFakeSCM("MSSQLSERVER"), UserScope: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	res, err := m.Reload()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) == 0 {
		t.Fatal("user-scope Reload must reject Type=scm")
	}
	if !strings.Contains(strings.Join(res.Errors, "\n"), "Type=scm is only supported in the system manager") {
		t.Fatalf("errors = %v", res.Errors)
	}
	if _, err := m.Status("mssql"); err == nil {
		t.Fatal("Type=scm must not be loaded in a user manager")
	}
	if specs := launch.specs(); len(specs) != 0 {
		t.Fatalf("must not CreateProcess: %+v", specs)
	}
}

func TestUserScopeStartRejectsTypeSCM(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	scm := newFakeSCM("MSSQLSERVER")
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "units"), 0o755); err != nil {
		t.Fatal(err)
	}
	m, err := New(Config{BaseDir: dir, Launch: launch, SCM: scm, UserScope: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	rep := unit.ParseUnit("mssql.service", `
[Service]
Type=scm
ServiceName=MSSQLSERVER
`)
	if rep.HasError() {
		t.Fatalf("parse: %v", rep.Errors())
	}
	g, err := core.Build([]*unit.Unit{rep.Unit})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.units["mssql.service"] = &unitRuntime{unit: rep.Unit, state: core.Inactive}
	m.graph = g
	m.mu.Unlock()

	_, err = m.Start(context.Background(), "mssql")
	if err == nil || !strings.Contains(err.Error(), "system manager") {
		t.Fatalf("start err = %v", err)
	}
	if scm.nstarts() != 0 {
		t.Fatalf("user-scope must not call StartService: %d", scm.nstarts())
	}
	if specs := launch.specs(); len(specs) != 0 {
		t.Fatalf("must not CreateProcess: %+v", specs)
	}
}

func TestTypeSCMRestartOnStartFailure(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	scm := newFakeSCM("MSSQLSERVER")
	scm.failStarts = 1
	fk := timers.NewFake(time.Time{})
	m := managerWithSCMClock(t, launch, scm, fk.Clock(), map[string]string{
		"mssql.service": `
[Service]
Type=scm
ServiceName=MSSQLSERVER
Restart=on-failure
RestartSec=5s
`,
	})
	if _, err := m.Start(context.Background(), "mssql"); err == nil {
		t.Fatal("first start should fail")
	}
	waitSub(t, m, "mssql.service", core.SubAutoRestart)
	advanceWait(t, fk, 5*time.Second)
	waitCond(t, func() bool {
		st, err := m.Status("mssql")
		return err == nil && st.Unit != nil && st.Unit.ActiveState == "active"
	})
	if scm.nstarts() < 2 {
		t.Fatalf("SCM starts = %d, want restart", scm.nstarts())
	}
	if specs := launch.specs(); len(specs) != 0 {
		t.Fatalf("must not CreateProcess: %+v", specs)
	}
}

func TestTypeSCMStartTimeoutFails(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	scm := newFakeSCM("MSSQLSERVER")
	scm.hangStart = true
	fk := timers.NewFake(time.Time{})
	m := managerWithSCMClock(t, launch, scm, fk.Clock(), map[string]string{
		"mssql.service": `
[Service]
Type=scm
ServiceName=MSSQLSERVER
TimeoutStartSec=5s
Restart=no
`,
	})
	errc := make(chan error, 1)
	go func() {
		_, err := m.Start(context.Background(), "mssql")
		errc <- err
	}()
	waitCond(t, fk.Waiting)
	advanceWait(t, fk, 5*time.Second)
	err := waitErr(t, errc)
	if err == nil {
		t.Fatal("timeout start must fail")
	}
	assertState(t, m, "mssql.service", core.Failed)
	if specs := launch.specs(); len(specs) != 0 {
		t.Fatalf("must not CreateProcess: %+v", specs)
	}
}

func TestTypeSCMStubLauncherNeverCreateProcessOnSCMError(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	m := managerWithSCM(t, launch, runtime.DefaultSCM(), map[string]string{
		"mssql.service": `
[Service]
Type=scm
ServiceName=winunitd-p7-no-such-service
`,
	})
	if _, err := m.Start(context.Background(), "mssql"); err == nil {
		t.Fatal("DefaultSCM on this GOOS must not start a real service")
	}
	if specs := launch.specs(); len(specs) != 0 {
		t.Fatalf("Type=scm must not CreateProcess: %+v", specs)
	}
}

func managerWithSCM(t *testing.T, launch runtime.Launcher, scm runtime.SCM, files map[string]string) *Manager {
	t.Helper()
	return managerWithSCMClock(t, launch, scm, timers.Clock{}, files)
}

func managerWithSCMClock(t *testing.T, launch runtime.Launcher, scm runtime.SCM, clk timers.Clock, files map[string]string) *Manager {
	t.Helper()
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		writeUnit(t, units, name, body)
	}
	m, err := New(Config{BaseDir: dir, Launch: launch, SCM: scm, Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	res, err := m.Reload()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) > 0 {
		t.Fatalf("reload errors: %v", res.Errors)
	}
	t.Cleanup(func() { stopAll(m) })
	return m
}

type fakeSCM struct {
	mu         sync.Mutex
	svcs       map[string]*fakeSCMSvc
	starts     int
	startDone  []time.Time
	startDelay time.Duration
	failStarts int
	hangStart  bool
}

type fakeSCMSvc struct {
	state runtime.SCMState
	pid   int
}

func newFakeSCM(names ...string) *fakeSCM {
	f := &fakeSCM{svcs: make(map[string]*fakeSCMSvc)}
	for _, name := range names {
		f.svcs[name] = &fakeSCMSvc{state: runtime.SCMStopped}
	}
	return f
}

func (f *fakeSCM) set(name string, state runtime.SCMState, pid int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.svcs[name] = &fakeSCMSvc{state: state, pid: pid}
}

func (f *fakeSCM) nstarts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts
}

func (f *fakeSCM) lastStartDone() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.startDone) == 0 {
		return time.Time{}
	}
	return f.startDone[len(f.startDone)-1]
}

func (f *fakeSCM) lookup(name string) (*fakeSCMSvc, error) {
	s, ok := f.svcs[name]
	if !ok {
		return nil, fmt.Errorf("open service %s: not found", name)
	}
	return s, nil
}

func (f *fakeSCM) Start(ctx context.Context, name string, timeout time.Duration) (runtime.SCMStatus, error) {
	if f.hangStart {
		wait := timeout
		if wait <= 0 {
			wait = 30 * time.Second
		}
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return runtime.SCMStatus{}, fmt.Errorf("timeout waiting for %s to become running: %w", name, ctx.Err())
		case <-timer.C:
			return runtime.SCMStatus{}, fmt.Errorf("timeout waiting for %s to become running", name)
		}
	}
	if f.startDelay > 0 {
		timer := time.NewTimer(f.startDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return runtime.SCMStatus{}, ctx.Err()
		case <-timer.C:
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	if f.failStarts > 0 {
		f.failStarts--
		return runtime.SCMStatus{State: runtime.SCMStopped}, fmt.Errorf("StartService %s: refused", name)
	}
	s, err := f.lookup(name)
	if err != nil {
		return runtime.SCMStatus{}, err
	}
	if s.state != runtime.SCMRunning {
		s.state = runtime.SCMRunning
		s.pid = 42
	}
	f.startDone = append(f.startDone, time.Now())
	return runtime.SCMStatus{State: s.state, PID: s.pid}, nil
}

func (f *fakeSCM) Stop(ctx context.Context, name string, timeout time.Duration) (runtime.SCMStatus, error) {
	_ = ctx
	_ = timeout
	f.mu.Lock()
	defer f.mu.Unlock()
	s, err := f.lookup(name)
	if err != nil {
		return runtime.SCMStatus{}, err
	}
	s.state = runtime.SCMStopped
	s.pid = 0
	return runtime.SCMStatus{State: s.state}, nil
}

func (f *fakeSCM) Query(name string) (runtime.SCMStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, err := f.lookup(name)
	if err != nil {
		return runtime.SCMStatus{}, err
	}
	return runtime.SCMStatus{State: s.state, PID: s.pid}, nil
}

func (f *fakeLauncher) firstStartAt() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.firstAt.IsZero() {
		return time.Time{}
	}
	return f.firstAt
}
