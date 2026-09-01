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

const testTaskName = `\Backups\LegacyBackup`

func TestTypeScheduledTaskDoesNotCreateProcess(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	ts := newFakeTasks(testTaskName)
	m := managerWithTasks(t, launch, ts, map[string]string{
		"legacy-backup.service": `
[Service]
Type=scheduled-task
TaskName=\Backups\LegacyBackup
`,
	})
	st, err := m.Start(context.Background(), "legacy-backup")
	if err != nil {
		t.Fatal(err)
	}
	if st.ActiveState != "active" {
		t.Fatalf("start = %+v", st)
	}
	if specs := launch.specs(); len(specs) != 0 {
		t.Fatalf("Type=scheduled-task must not CreateProcess: %+v", specs)
	}
	if got := ts.nstarts(); got != 1 {
		t.Fatalf("task starts = %d", got)
	}

	status, err := m.Status("legacy-backup")
	if err != nil {
		t.Fatal(err)
	}
	if status.Unit == nil || status.Unit.ActiveState != "active" {
		t.Fatalf("status = %+v", status.Unit)
	}
	if status.Unit.InvocationID != "" {
		t.Fatalf("Type=scheduled-task must omit InvocationID: %+v", status.Unit)
	}
	if status.Unit.MainPID != 42 {
		t.Fatalf("MainPID = %d", status.Unit.MainPID)
	}
}

func TestTypeScheduledTaskAlreadyRunningStartIsOK(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	ts := newFakeTasks(testTaskName)
	ts.set(testTaskName, runtime.TaskRunning, 1, 7)
	m := managerWithTasks(t, launch, ts, map[string]string{
		"legacy-backup.service": `
[Service]
Type=scheduled-task
TaskName=\Backups\LegacyBackup
`,
	})
	st, err := m.Start(context.Background(), "legacy-backup")
	if err != nil {
		t.Fatal(err)
	}
	if st.ActiveState != "active" {
		t.Fatalf("start = %+v", st)
	}
	again, err := m.Start(context.Background(), "legacy-backup")
	if err != nil {
		t.Fatal(err)
	}
	if again.ActiveState != "active" {
		t.Fatalf("second start = %+v", again)
	}
	if specs := launch.specs(); len(specs) != 0 {
		t.Fatalf("Type=scheduled-task must not CreateProcess: %+v", specs)
	}
}

func TestTypeScheduledTaskStopInactiveAndAlreadyStopped(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	ts := newFakeTasks(testTaskName)
	m := managerWithTasks(t, launch, ts, map[string]string{
		"legacy-backup.service": `
[Service]
Type=scheduled-task
TaskName=\Backups\LegacyBackup
`,
	})
	if _, err := m.Start(context.Background(), "legacy-backup"); err != nil {
		t.Fatal(err)
	}
	st, err := m.Stop("legacy-backup")
	if err != nil {
		t.Fatal(err)
	}
	if st.ActiveState != "inactive" {
		t.Fatalf("stop = %+v", st)
	}
	status, err := m.Status("legacy-backup")
	if err != nil {
		t.Fatal(err)
	}
	if status.Unit.ActiveState != "inactive" {
		t.Fatalf("status after stop = %+v", status.Unit)
	}
	again, err := m.Stop("legacy-backup")
	if err != nil {
		t.Fatal(err)
	}
	if again.ActiveState != "inactive" {
		t.Fatalf("already-stopped = %+v", again)
	}
	if specs := launch.specs(); len(specs) != 0 {
		t.Fatalf("Type=scheduled-task must not CreateProcess: %+v", specs)
	}
}

func TestTypeScheduledTaskAfterWaitsUntilRunning(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	ts := newFakeTasks(testTaskName)
	ts.startDelay = 80 * time.Millisecond
	m := managerWithTasks(t, launch, ts, map[string]string{
		"legacy-backup.service": `
[Service]
Type=scheduled-task
TaskName=\Backups\LegacyBackup
`,
		"app.service": `
[Unit]
Requires=legacy-backup.service
After=legacy-backup.service
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
	done := ts.lastStartDone()
	if done.IsZero() {
		t.Fatal("task start was not recorded")
	}
	started := launch.firstStartAt()
	if started.IsZero() || started.Before(done) {
		t.Fatalf("Type=simple started at %s before task reported running at %s", started, done)
	}
}

func TestUserScopeReloadRejectsTypeScheduledTask(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, units, "legacy-backup.service", `
[Service]
Type=scheduled-task
TaskName=\Backups\LegacyBackup
`)
	m, err := New(Config{BaseDir: dir, Launch: launch, Tasks: newFakeTasks(testTaskName), UserScope: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	res, err := m.Reload()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) == 0 {
		t.Fatal("user-scope Reload must reject Type=scheduled-task")
	}
	if !strings.Contains(strings.Join(res.Errors, "\n"), "Type=scheduled-task is only supported in the system manager") {
		t.Fatalf("errors = %v", res.Errors)
	}
	if _, err := m.Status("legacy-backup"); err == nil {
		t.Fatal("Type=scheduled-task must not be loaded in a user manager")
	}
	if specs := launch.specs(); len(specs) != 0 {
		t.Fatalf("must not CreateProcess: %+v", specs)
	}
}

func TestUserScopeStartRejectsTypeScheduledTask(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	ts := newFakeTasks(testTaskName)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "units"), 0o755); err != nil {
		t.Fatal(err)
	}
	m, err := New(Config{BaseDir: dir, Launch: launch, Tasks: ts, UserScope: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	rep := unit.ParseUnit("legacy-backup.service", `
[Service]
Type=scheduled-task
TaskName=\Backups\LegacyBackup
`)
	if rep.HasError() {
		t.Fatalf("parse: %v", rep.Errors())
	}
	g, err := core.Build([]*unit.Unit{rep.Unit})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.units["legacy-backup.service"] = &unitRuntime{unit: rep.Unit, state: core.Inactive}
	m.graph = g
	m.mu.Unlock()

	_, err = m.Start(context.Background(), "legacy-backup")
	if err == nil || !strings.Contains(err.Error(), "system manager") {
		t.Fatalf("start err = %v", err)
	}
	if ts.nstarts() != 0 {
		t.Fatalf("user-scope must not call Run: %d", ts.nstarts())
	}
	if specs := launch.specs(); len(specs) != 0 {
		t.Fatalf("must not CreateProcess: %+v", specs)
	}
}

func TestTypeScheduledTaskRestartOnStartFailure(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	ts := newFakeTasks(testTaskName)
	ts.failStarts = 1
	fk := timers.NewFake(time.Time{})
	m := managerWithTasksClock(t, launch, ts, fk.Clock(), map[string]string{
		"legacy-backup.service": `
[Service]
Type=scheduled-task
TaskName=\Backups\LegacyBackup
Restart=on-failure
RestartSec=5s
`,
	})
	if _, err := m.Start(context.Background(), "legacy-backup"); err == nil {
		t.Fatal("first start should fail")
	}
	waitSub(t, m, "legacy-backup.service", core.SubAutoRestart)
	advanceWait(t, fk, 5*time.Second)
	waitCond(t, func() bool {
		st, err := m.Status("legacy-backup")
		return err == nil && st.Unit != nil && st.Unit.ActiveState == "active"
	})
	if ts.nstarts() < 2 {
		t.Fatalf("task starts = %d, want restart", ts.nstarts())
	}
	if specs := launch.specs(); len(specs) != 0 {
		t.Fatalf("must not CreateProcess: %+v", specs)
	}
}

func TestTypeScheduledTaskStartTimeoutFails(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	ts := newFakeTasks(testTaskName)
	ts.hangStart = true
	fk := timers.NewFake(time.Time{})
	m := managerWithTasksClock(t, launch, ts, fk.Clock(), map[string]string{
		"legacy-backup.service": `
[Service]
Type=scheduled-task
TaskName=\Backups\LegacyBackup
TimeoutStartSec=5s
Restart=no
`,
	})
	errc := make(chan error, 1)
	go func() {
		_, err := m.Start(context.Background(), "legacy-backup")
		errc <- err
	}()
	waitCond(t, fk.Waiting)
	advanceWait(t, fk, 5*time.Second)
	err := waitErr(t, errc)
	if err == nil {
		t.Fatal("timeout start must fail")
	}
	assertState(t, m, "legacy-backup.service", core.Failed)
	if specs := launch.specs(); len(specs) != 0 {
		t.Fatalf("must not CreateProcess: %+v", specs)
	}
}

func TestTypeScheduledTaskMissingTaskStartFails(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	ts := newFakeTasks()
	m := managerWithTasks(t, launch, ts, map[string]string{
		"legacy-backup.service": `
[Service]
Type=scheduled-task
TaskName=\Backups\LegacyBackup
Restart=no
`,
	})
	if _, err := m.Start(context.Background(), "legacy-backup"); err == nil {
		t.Fatal("missing task must fail start")
	}
	assertState(t, m, "legacy-backup.service", core.Failed)
	status, err := m.Status("legacy-backup")
	if err != nil {
		t.Fatal(err)
	}
	if status.Unit == nil || status.Unit.ActiveState != "failed" {
		t.Fatalf("status for missing task = %+v", status.Unit)
	}
	if specs := launch.specs(); len(specs) != 0 {
		t.Fatalf("must not CreateProcess: %+v", specs)
	}
}

func TestTypeScheduledTaskStubNeverCreateProcess(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	m := managerWithTasks(t, launch, runtime.DefaultTaskScheduler(), map[string]string{
		"legacy-backup.service": `
[Service]
Type=scheduled-task
TaskName=\WinUnitdTS1\no-such-task-ts1
`,
	})
	if _, err := m.Start(context.Background(), "legacy-backup"); err == nil {
		t.Fatal("DefaultTaskScheduler on this GOOS must not start a real task")
	}
	if specs := launch.specs(); len(specs) != 0 {
		t.Fatalf("Type=scheduled-task must not CreateProcess: %+v", specs)
	}
}

func managerWithTasks(t *testing.T, launch runtime.Launcher, ts runtime.TaskScheduler, files map[string]string) *Manager {
	t.Helper()
	return managerWithTasksClock(t, launch, ts, timers.Clock{}, files)
}

func managerWithTasksClock(t *testing.T, launch runtime.Launcher, ts runtime.TaskScheduler, clk timers.Clock, files map[string]string) *Manager {
	t.Helper()
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		writeUnit(t, units, name, body)
	}
	m, err := New(Config{BaseDir: dir, Launch: launch, Tasks: ts, Clock: clk})
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

type fakeTasks struct {
	mu         sync.Mutex
	tasks      map[string]*fakeTask
	starts     int
	startDone  []time.Time
	startDelay time.Duration
	failStarts int
	hangStart  bool
}

type fakeTask struct {
	state     runtime.TaskState
	instances int
	pid       int
}

func newFakeTasks(names ...string) *fakeTasks {
	f := &fakeTasks{tasks: make(map[string]*fakeTask)}
	for _, name := range names {
		f.tasks[name] = &fakeTask{state: runtime.TaskReady}
	}
	return f
}

func (f *fakeTasks) set(name string, state runtime.TaskState, instances, pid int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tasks[name] = &fakeTask{state: state, instances: instances, pid: pid}
}

func (f *fakeTasks) nstarts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts
}

func (f *fakeTasks) lastStartDone() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.startDone) == 0 {
		return time.Time{}
	}
	return f.startDone[len(f.startDone)-1]
}

func (f *fakeTasks) lookup(name string) (*fakeTask, error) {
	s, ok := f.tasks[name]
	if !ok {
		return nil, fmt.Errorf("open task %s: not found", name)
	}
	return s, nil
}

func (f *fakeTasks) Start(ctx context.Context, name string, timeout time.Duration) (runtime.TaskStatus, error) {
	if f.hangStart {
		wait := timeout
		if wait <= 0 {
			wait = 30 * time.Second
		}
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return runtime.TaskStatus{}, fmt.Errorf("timeout waiting for %s to become active: %w", name, ctx.Err())
		case <-timer.C:
			return runtime.TaskStatus{}, fmt.Errorf("timeout waiting for %s to become active", name)
		}
	}
	if f.startDelay > 0 {
		timer := time.NewTimer(f.startDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return runtime.TaskStatus{}, ctx.Err()
		case <-timer.C:
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	if f.failStarts > 0 {
		f.failStarts--
		return runtime.TaskStatus{State: runtime.TaskReady}, fmt.Errorf("Run %s: refused", name)
	}
	s, err := f.lookup(name)
	if err != nil {
		return runtime.TaskStatus{}, err
	}
	if s.state != runtime.TaskRunning {
		s.state = runtime.TaskRunning
		s.instances = 1
		s.pid = 42
	}
	f.startDone = append(f.startDone, time.Now())
	return runtime.TaskStatus{State: s.state, Instances: s.instances, PID: s.pid}, nil
}

func (f *fakeTasks) Stop(ctx context.Context, name string, timeout time.Duration) (runtime.TaskStatus, error) {
	_ = ctx
	_ = timeout
	f.mu.Lock()
	defer f.mu.Unlock()
	s, err := f.lookup(name)
	if err != nil {
		return runtime.TaskStatus{}, err
	}
	s.state = runtime.TaskReady
	s.instances = 0
	s.pid = 0
	return runtime.TaskStatus{State: s.state}, nil
}

func (f *fakeTasks) Query(name string) (runtime.TaskStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, err := f.lookup(name)
	if err != nil {
		return runtime.TaskStatus{}, err
	}
	return runtime.TaskStatus{State: s.state, Instances: s.instances, PID: s.pid}, nil
}
