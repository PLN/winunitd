package manager

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/timers"
)

func TestShutdownStopsAfterOrderedServices(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	m := managerWith(t, launch, map[string]string{
		"web.service": `
[Unit]
Requires=db.service
After=db.service
[Service]
ExecStart=C:\Tools\web.exe
WorkingDirectory=C:\Tools
`,
		"db.service": `
[Service]
ExecStart=C:\Tools\db.exe
WorkingDirectory=C:\Tools
`,
	})
	if _, err := m.Start(context.Background(), "web"); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "web.service", core.Active)
	assertState(t, m, "db.service", core.Active)

	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "web.service", core.Inactive)
	assertState(t, m, "db.service", core.Inactive)
	got := launch.stopped()
	if len(got) < 2 || got[0] != "web.service" || got[1] != "db.service" {
		t.Fatalf("stop order = %v, want web then db", got)
	}
}

func TestStopTargetDoesNotStopWants(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	m := managerWith(t, launch, map[string]string{
		"app.target": `
[Unit]
Wants=web.service db.service
`,
		"web.service": `
[Unit]
After=db.service
[Service]
ExecStart=C:\Tools\web.exe
WorkingDirectory=C:\Tools
`,
		"db.service": `
[Service]
ExecStart=C:\Tools\db.exe
WorkingDirectory=C:\Tools
`,
	})
	if _, err := m.Start(context.Background(), "app.target"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stop("app.target"); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "app.target", core.Inactive)
	assertState(t, m, "web.service", core.Active)
	assertState(t, m, "db.service", core.Active)
	if containsString(launch.stopped(), "web.service") || containsString(launch.stopped(), "db.service") {
		t.Fatalf("stopping a target must not stop its Wants=: %v", launch.stopped())
	}
}

func TestStopTargetStopsPartOfInReverse(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	m := managerWith(t, launch, map[string]string{
		"app.target": `
[Unit]
Description=App
`,
		"web.service": `
[Unit]
PartOf=app.target
After=db.service
[Service]
ExecStart=C:\Tools\web.exe
WorkingDirectory=C:\Tools
`,
		"db.service": `
[Unit]
PartOf=app.target
[Service]
ExecStart=C:\Tools\db.exe
WorkingDirectory=C:\Tools
`,
	})
	if _, err := m.Start(context.Background(), "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(context.Background(), "db"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(context.Background(), "app.target"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stop("app.target"); err != nil {
		t.Fatal(err)
	}
	got := launch.stopped()
	if len(got) < 2 || got[0] != "web.service" || got[1] != "db.service" {
		t.Fatalf("stop order = %v, want web then db", got)
	}
	assertState(t, m, "app.target", core.Inactive)
	assertState(t, m, "web.service", core.Inactive)
	assertState(t, m, "db.service", core.Inactive)
}

func TestStopServiceStopsReverseRequires(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	m := managerWith(t, launch, map[string]string{
		"web.service": `
[Unit]
Requires=db.service
After=db.service
[Service]
ExecStart=C:\Tools\web.exe
WorkingDirectory=C:\Tools
`,
		"db.service": `
[Service]
ExecStart=C:\Tools\db.exe
WorkingDirectory=C:\Tools
`,
	})
	if _, err := m.Start(context.Background(), "web"); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "web.service", core.Active)
	assertState(t, m, "db.service", core.Active)
	if _, err := m.Stop("db"); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "web.service", core.Inactive)
	assertState(t, m, "db.service", core.Inactive)
	got := launch.stopped()
	if len(got) < 2 || got[0] != "web.service" || got[1] != "db.service" {
		t.Fatalf("stop order = %v, want web then db", got)
	}
}

func TestStopServiceLeavesForwardRequires(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	m := managerWith(t, launch, map[string]string{
		"web.service": `
[Unit]
Requires=db.service
After=db.service
[Service]
ExecStart=C:\Tools\web.exe
WorkingDirectory=C:\Tools
`,
		"db.service": `
[Service]
ExecStart=C:\Tools\db.exe
WorkingDirectory=C:\Tools
`,
	})
	if _, err := m.Start(context.Background(), "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stop("web"); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "web.service", core.Inactive)
	assertState(t, m, "db.service", core.Active)
	if containsString(launch.stopped(), "db.service") {
		t.Fatal("stopping a consumer must not stop its forward Requires=")
	}
}

func TestStopServiceDoesNotStopAfterDependents(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	m := managerWith(t, launch, map[string]string{
		"web.service": `
[Unit]
After=db.service
[Service]
ExecStart=C:\Tools\web.exe
WorkingDirectory=C:\Tools
`,
		"db.service": `
[Service]
ExecStart=C:\Tools\db.exe
WorkingDirectory=C:\Tools
`,
	})
	if _, err := m.Start(context.Background(), "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(context.Background(), "db"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stop("db"); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "db.service", core.Inactive)
	assertState(t, m, "web.service", core.Active)
	if containsString(launch.stopped(), "web.service") {
		t.Fatal("stopping a service must not stop After= dependents")
	}
}

func TestShutdownDisarmsTimers(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	m, fk := managerWithFake(t, launch, map[string]string{
		"job.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\job.exe
WorkingDirectory=C:\Tools
`,
		"job.timer": `
[Timer]
OnStartupSec=5s
`,
	})
	if _, err := m.Start(context.Background(), "job.timer"); err != nil {
		t.Fatal(err)
	}
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.engine.Armed("job.timer") {
		t.Fatal("timer still armed after shutdown")
	}
	fk.Advance(5 * time.Second)
	if containsString(launch.units(), "job.service") {
		t.Fatal("disarmed timer must not activate the service")
	}
}

func TestShutdownCancelsPendingRestart(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(2), holdAutoExit: true}
	m, fk := managerWithFake(t, launch, map[string]string{
		"foo.service": `
[Service]
Type=simple
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
Restart=always
RestartSec=1s
`,
	})
	if _, err := m.Start(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
	launch.releaseExits()
	waitSub(t, m, "foo.service", core.SubAutoRestart)
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	n := launch.nstarts()
	fk.Advance(time.Second)
	if got := launch.nstarts(); got != n {
		t.Fatalf("restart after shutdown: starts %d -> %d", n, got)
	}
}

// hangJournalLauncher is a fakeLauncher whose stdout never reaches EOF,
// so journal.Wait remains owned until the test closes its pipe.
type hangJournalLauncher struct {
	fakeLauncher
	pw    *io.PipeWriter
	pipes []*io.PipeWriter
	last  *fakeProc
}

func (f *hangJournalLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	pr, pw := io.Pipe()
	f.mu.Lock()
	f.starts = append(f.starts, spec)
	f.pw = pw
	f.pipes = append(f.pipes, pw)
	pid := f.pid
	f.mu.Unlock()
	if pid == 0 {
		pid = 1
	}
	job, err := runtime.OpenUnitJob()
	if err != nil {
		_ = pw.Close()
		return nil, err
	}
	p := &fakeProc{
		name:   spec.Unit,
		rec:    &f.fakeLauncher,
		pid:    pid,
		job:    job,
		done:   make(chan struct{}),
		stdout: io.NopCloser(pr),
		stderr: io.NopCloser(strings.NewReader("")),
	}
	f.mu.Lock()
	f.last = p
	f.mu.Unlock()
	return p, nil
}

func (f *hangJournalLauncher) dieLast(code uint32) {
	f.mu.Lock()
	p := f.last
	f.mu.Unlock()
	if p != nil {
		p.die(code)
	}
}

func (f *hangJournalLauncher) nstarts() int {
	return len(f.units())
}

func (f *hangJournalLauncher) closePipes() {
	f.mu.Lock()
	pipes := append([]*io.PipeWriter(nil), f.pipes...)
	f.mu.Unlock()
	for _, pw := range pipes {
		if pw != nil {
			_ = pw.Close()
		}
	}
}

func TestStopUnitReleasesOpLockDuringJournalWait(t *testing.T) {
	t.Parallel()
	launch := &hangJournalLauncher{}
	m, fk := managerWithFake(t, launch, map[string]string{
		"foo.service": `
[Service]
Type=simple
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
TimeoutStopSec=30s
`,
	})
	t.Cleanup(launch.closePipes)
	if _, err := m.Start(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}

	stopErr := make(chan error, 1)
	go func() {
		_, err := m.Stop("foo")
		stopErr <- err
	}()

	waitCond(t, func() bool {
		m.mu.Lock()
		st := m.stateOfLocked("foo.service")
		m.mu.Unlock()
		if st != core.Deactivating {
			return false
		}
		unlock, ok := m.ops.tryLock("foo.service")
		if !ok {
			return false
		}
		unlock()
		select {
		case <-stopErr:
			return false
		default:
			return true
		}
	})

	fk.Advance(30 * time.Second)
	select {
	case err := <-stopErr:
		if err == nil {
			t.Fatal("Stop did not report exhausted journal finalization budget")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return after TimeoutStopSec")
	}

	if _, err := m.Start(context.Background(), "foo"); err == nil {
		t.Fatal("replacement admitted while output remained owned")
	}
	launch.closePipes()
	if _, err := m.Stop("foo"); err != nil {
		t.Fatalf("cleanup retry: %v", err)
	}
	if _, err := m.Start(context.Background(), "foo"); err != nil {
		t.Fatalf("replacement after cleanup: %v", err)
	}
}

func TestLaunchUnitOpRetainsHungJournalAfterSelfExit(t *testing.T) {
	t.Parallel()
	launch := &hangJournalLauncher{}
	m, fk := managerWithFake(t, launch, map[string]string{
		"foo.service": `
[Service]
Type=simple
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
TimeoutStopSec=30s
`,
	})
	t.Cleanup(launch.closePipes)
	if _, err := m.Start(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
	assertStopTimeout(t, m, "foo.service", 30*time.Second)
	launch.dieLast(1)
	waitCond(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		rt := m.units["foo.service"]
		return rt != nil && (rt.proc == nil || !rt.proc.Alive())
	})

	startErr := make(chan error, 1)
	go func() {
		_, err := m.Start(context.Background(), "foo")
		startErr <- err
	}()

	waitHungLaunchJournal(t, m, fk, launch, "foo.service")
	select {
	case err := <-startErr:
		t.Fatalf("second Start returned before TimeoutStopSec: %v", err)
	default:
	}

	fk.Advance(30 * time.Second)
	select {
	case err := <-startErr:
		if err == nil {
			t.Fatal("replacement succeeded with unfinished prior output")
		}
		if launch.nstarts() != 1 {
			t.Fatal("created replacement before capture cleanup")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second Start did not return after TimeoutStopSec")
	}

	// A late exit watcher can briefly acquire the same lock after Start
	// returns. Require bounded acquisition, not an idle lock at one instant.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	unlock, err := m.ops.lockContext(ctx, "foo.service")
	if err != nil {
		t.Fatalf("op lock did not become available: %v", err)
	}
	unlock()
}

func TestLaunchUnitOpRetainsHungJournalOnAutoRestart(t *testing.T) {
	t.Parallel()
	launch := &hangJournalLauncher{}
	m, fk := managerWithFake(t, launch, map[string]string{
		"foo.service": `
[Unit]
StartLimitBurst=0
[Service]
Type=simple
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
Restart=always
RestartSec=0
TimeoutStopSec=30s
`,
	})
	t.Cleanup(launch.closePipes)
	if _, err := m.Start(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
	assertStopTimeout(t, m, "foo.service", 30*time.Second)
	launch.dieLast(1)

	waitHungLaunchJournal(t, m, fk, launch, "foo.service")
	fk.Advance(30 * time.Second)
	waitCond(t, func() bool {
		if launch.nstarts() != 1 {
			return false
		}
		unlock, ok := m.ops.tryLock("foo.service")
		if !ok {
			return false
		}
		unlock()
		return true
	})
}

func assertStopTimeout(t *testing.T, m *Manager, name string, want time.Duration) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	rt := m.units[name]
	if rt == nil || rt.unit == nil {
		t.Fatalf("no unit %s", name)
	}
	if got := stopTimeout(rt.unit); got != want {
		t.Fatalf("stopTimeout(%s) = %v, want %v", name, got, want)
	}
}

func waitHungLaunchJournal(t *testing.T, m *Manager, fk *timers.Fake, launch *hangJournalLauncher, name string) {
	t.Helper()
	// The prior capture must be resolved before another native launch.
	waitCond(t, func() bool {
		m.mu.Lock()
		priorProcessReleased := m.units[name] != nil && m.units[name].proc == nil
		m.mu.Unlock()
		if launch.nstarts() != 1 || !priorProcessReleased || !fk.WaitingAt(30*time.Second) {
			return false
		}
		unlock, ok := m.ops.tryLock(name)
		if ok {
			unlock()
			return false
		}
		return true
	})
}

func TestShutdownWaitsForLateLaunchAndClosesAdmission(t *testing.T) {
	const name = "shutdown-launch.service"
	l := &lateFailedStopLauncher{entered: make(chan struct{}), release: make(chan struct{})}
	m := managerWith(t, l, map[string]string{name: "[Service]\nExecStart=C:\\Tools\\worker.exe\n"})
	var once sync.Once
	release := func() { once.Do(func() { close(l.release) }) }
	t.Cleanup(release)
	started := make(chan error, 1)
	go func() { _, err := m.Start(context.Background(), name); started <- err }()
	select {
	case <-l.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("launch did not enter")
	}
	p := l.proc
	p.fail.Store(false)
	t.Cleanup(func() { _ = p.Process.Stop(time.Second) })
	done := make(chan error, 1)
	go func() { done <- m.Shutdown(context.Background()) }()
	waitCond(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.closed
	})
	select {
	case err := <-done:
		t.Fatalf("shutdown returned before launch completion: %v", err)
	default:
	}
	if _, err := m.Start(context.Background(), name); err == nil {
		t.Fatal("shutdown admitted a new start")
	}
	release()
	if err := waitErr(t, started); err == nil {
		t.Fatal("superseded launch reported success")
	}
	if err := waitErr(t, done); err != nil {
		t.Fatal(err)
	}
	if p.Alive() {
		t.Fatal("shutdown returned with late process alive")
	}
	if _, err := m.Start(context.Background(), name); err == nil {
		t.Fatal("completed shutdown admitted a new start")
	}
}

func TestShutdownDeadlinePreservesPendingLaunch(t *testing.T) {
	const name = "deadline-launch.service"
	l := &lateFailedStopLauncher{entered: make(chan struct{}), release: make(chan struct{})}
	m := managerWith(t, l, map[string]string{name: "[Service]\nExecStart=C:\\Tools\\worker.exe\n"})
	var once sync.Once
	release := func() { once.Do(func() { close(l.release) }) }
	t.Cleanup(release)
	started := make(chan error, 1)
	go func() { _, err := m.Start(context.Background(), name); started <- err }()
	select {
	case <-l.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("launch did not enter")
	}
	p := l.proc
	p.fail.Store(false)
	t.Cleanup(func() { _ = p.Process.Stop(time.Second) })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- m.Shutdown(ctx) }()
	if err := waitErr(t, done); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown deadline result = %v", err)
	}
	m.mu.Lock()
	pending := m.units[name].operations != 0
	m.mu.Unlock()
	if !pending || !p.Alive() {
		t.Fatal("deadline discarded pending launch")
	}
	release()
	if err := waitErr(t, started); err == nil {
		t.Fatal("late start reported success")
	}
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatal("shutdown retry", err)
	}
	if p.Alive() {
		t.Fatal("late launch survived shutdown retry")
	}
}

type shutdownBlockedLauncher struct {
	fakeLauncher
	proc *blockedStopProcess
}

func (l *shutdownBlockedLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	p, err := l.fakeLauncher.Start(ctx, spec)
	if err != nil {
		return nil, err
	}
	l.proc = &blockedStopProcess{Process: p, release: make(chan struct{})}
	return l.proc, nil
}

func TestShutdownDeadlinePreservesPendingStop(t *testing.T) {
	const name = "deadline-stop.service"
	l := &shutdownBlockedLauncher{}
	m := managerWith(t, l, map[string]string{name: "[Service]\nExecStart=C:\\Tools\\worker.exe\nTimeoutStopSec=30s\n"})
	if _, err := m.Start(context.Background(), name); err != nil {
		t.Fatal(err)
	}
	p := l.proc
	var once sync.Once
	release := func() { once.Do(func() { close(p.release) }) }
	t.Cleanup(func() { release(); _ = p.Process.Stop(time.Second) })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- m.Shutdown(ctx) }()
	if err := waitErr(t, done); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown deadline result = %v", err)
	}
	m.mu.Lock()
	rt := m.units[name]
	retained := rt.proc == p && rt.cleanupPending()
	m.mu.Unlock()
	if !retained || !p.Alive() || p.calls.Load() != 1 {
		t.Fatal("shutdown discarded pending termination")
	}
	release()
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatal("shutdown retry", err)
	}
	if p.Alive() {
		t.Fatal("pending stop survived retry")
	}
}

func TestShutdownDeadlineBoundsJournalWait(t *testing.T) {
	const name = "deadline-journal.service"
	l := &hangJournalLauncher{}
	m := managerWith(t, l, map[string]string{name: "[Service]\nExecStart=C:\\Tools\\worker.exe\nTimeoutStopSec=30s\n"})
	if _, err := m.Start(context.Background(), name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.pw.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- m.Shutdown(ctx) }()
	if err := waitErr(t, done); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown journal deadline result = %v", err)
	}
}
