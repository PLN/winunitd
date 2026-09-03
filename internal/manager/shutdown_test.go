package manager

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/runtime"
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
// so journal.Wait blocks until abandoned (issues #68 and #83).
type hangJournalLauncher struct {
	fakeLauncher
	pw   *io.PipeWriter
	last *fakeProc
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
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return after TimeoutStopSec")
	}

	if _, err := m.Start(context.Background(), "foo"); err != nil {
		t.Fatalf("later Start blocked: %v", err)
	}
	if launch.pw != nil {
		_ = launch.pw.Close()
	}
}

func TestLaunchUnitOpAbandonsHungJournalAfterSelfExit(t *testing.T) {
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
	if _, err := m.Start(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
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

	waitCond(t, func() bool {
		return fk.WaitingAt(30*time.Second) && launch.nstarts() == 1
	})
	select {
	case err := <-startErr:
		t.Fatalf("second Start returned before TimeoutStopSec: %v", err)
	default:
	}

	fk.Advance(30 * time.Second)
	select {
	case err := <-startErr:
		if err != nil {
			t.Fatalf("second Start: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second Start did not return after TimeoutStopSec")
	}

	unlock, ok := m.ops.tryLock("foo.service")
	if !ok {
		t.Fatal("op lock still held after second Start")
	}
	unlock()
	if launch.pw != nil {
		_ = launch.pw.Close()
	}
}

func TestLaunchUnitOpAbandonsHungJournalOnAutoRestart(t *testing.T) {
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
	if _, err := m.Start(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
	launch.dieLast(1)

	waitCond(t, func() bool {
		return fk.WaitingAt(30*time.Second) && launch.nstarts() == 1
	})
	fk.Advance(30 * time.Second)
	waitCond(t, func() bool { return launch.nstarts() >= 2 })

	unlock, ok := m.ops.tryLock("foo.service")
	if !ok {
		t.Fatal("op lock still held after Restart=always relaunch")
	}
	unlock()
	if launch.pw != nil {
		_ = launch.pw.Close()
	}
}
