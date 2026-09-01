package manager

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/runtime"
)

func TestIllegalStepDoesNotStampActiveFromFailed(t *testing.T) {
	t.Parallel()
	m := managerWith(t, &fakeLauncher{}, map[string]string{
		"foo.service": `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
	})
	m.mu.Lock()
	rt := m.units["foo.service"]
	rt.state = core.Failed
	rt.sub = core.SubNone
	rt.err = "already failed"
	if rt.step(core.EventStartSucceeded) {
		t.Fatal("Failed + start-succeeded must be rejected")
	}
	if rt.state != core.Failed || rt.sub != core.SubNone {
		t.Fatalf("state = %s/%s, want failed (not invented active)", rt.state, rt.sub)
	}
	if rt.proc != nil && rt.proc.Alive() {
		t.Fatal("illegal step must not leave a live proc marked active")
	}
	m.mu.Unlock()
}

func TestIllegalStepStopFinishedFromActivatingNoops(t *testing.T) {
	t.Parallel()
	m := managerWith(t, &fakeLauncher{}, map[string]string{
		"foo.service": `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
	})
	m.mu.Lock()
	rt := m.units["foo.service"]
	rt.state = core.Activating
	rt.sub = core.SubStart
	if rt.step(core.EventStopFinished) {
		t.Fatal("Activating + stop-finished must be rejected")
	}
	if rt.state != core.Activating || rt.sub != core.SubStart {
		t.Fatalf("state = %s/%s, want activating/start", rt.state, rt.sub)
	}
	m.mu.Unlock()
}

func TestIllegalStepWatchdogFromInactiveNoops(t *testing.T) {
	t.Parallel()
	m := managerWith(t, &fakeLauncher{}, map[string]string{
		"foo.service": `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
	})
	m.mu.Lock()
	rt := m.units["foo.service"]
	if rt.state != core.Inactive {
		t.Fatalf("fresh unit state = %s", rt.state)
	}
	if rt.step(core.EventWatchdogFailed) {
		t.Fatal("Inactive + watchdog-failed must be rejected")
	}
	if rt.state != core.Inactive {
		t.Fatalf("state = %s, want inactive (not invented failed)", rt.state)
	}
	m.mu.Unlock()
}

func TestIllegalStepConcurrentNoStateCorruption(t *testing.T) {
	t.Parallel()
	m := managerWith(t, &fakeLauncher{}, map[string]string{
		"foo.service": `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
	})
	m.mu.Lock()
	rt := m.units["foo.service"]
	rt.state = core.Activating
	rt.sub = core.SubStart
	m.mu.Unlock()

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.mu.Lock()
			defer m.mu.Unlock()
			_ = rt.step(core.EventStopFinished)
			_ = rt.step(core.EventStartSucceeded) // legal: Activating -> Active
			if rt.state == core.Active {
				_ = rt.step(core.EventStopFinished) // illegal from Active
			}
		}()
	}
	wg.Wait()

	m.mu.Lock()
	defer m.mu.Unlock()
	switch rt.state {
	case core.Activating, core.Active:
		// Illegal StopFinished never invented Inactive from Activating/Active.
	default:
		t.Fatalf("state = %s, corrupted by concurrent illegal steps", rt.state)
	}
	if rt.state == core.Inactive {
		t.Fatal("StopFinished from Activating/Active must not stamp inactive")
	}
}

func TestApplyRunLockedDoesNotFailLiveActive(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	m := managerWith(t, launch, map[string]string{
		"web.service": `
[Service]
ExecStart=C:\Tools\web.exe
WorkingDirectory=C:\Tools
`,
	})
	if _, err := m.Start(context.Background(), "web"); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "web.service", core.Active)
	m.mu.Lock()
	proc := m.procOfLocked("web.service")
	m.applyRunLocked(&core.Run{
		States: map[string]core.State{"web.service": core.Failed},
		Errors: map[string]error{"web.service": errors.New("required db.service failed")},
	})
	if m.stateOfLocked("web.service") != core.Active {
		t.Fatalf("state = %s, want active (live requirer)", m.stateOfLocked("web.service"))
	}
	if msg := m.errOfLocked("web.service"); msg != "" {
		t.Fatalf("error = %q, want empty on live active", msg)
	}
	m.mu.Unlock()
	if proc == nil || !proc.Alive() {
		t.Fatal("process must keep running")
	}
}

func TestRequiresFailureLeavesAlreadyActiveRunning(t *testing.T) {
	t.Parallel()
	launch := newGatedLauncher()
	m := managerWith(t, launch, map[string]string{
		"app.target": `
[Unit]
Requires=web.service late.service
Wants=probe.service
After=web.service late.service
`,
		"web.service": `
[Unit]
Requires=db.service
[Service]
ExecStart=C:\Tools\web.exe
WorkingDirectory=C:\Tools
`,
		"late.service": `
[Unit]
Requires=db.service
After=db.service
[Service]
ExecStart=C:\Tools\late.exe
WorkingDirectory=C:\Tools
`,
		"db.service": `
[Service]
ExecStart=C:\Tools\db.exe
WorkingDirectory=C:\Tools
`,
		"probe.service": `
[Unit]
After=web.service
[Service]
ExecStart=C:\Tools\probe.exe
WorkingDirectory=C:\Tools
`,
	})
	launch.failAfterProbe("db.service")
	_, err := m.Start(context.Background(), "app.target")
	if err == nil {
		t.Fatal("expected root error (app.target not yet started)")
	}
	assertState(t, m, "web.service", core.Active)
	assertState(t, m, "db.service", core.Failed)
	assertState(t, m, "late.service", core.Failed)
	assertState(t, m, "app.target", core.Failed)
	m.mu.Lock()
	proc := m.procOfLocked("web.service")
	lateProc := m.procOfLocked("late.service")
	m.mu.Unlock()
	if proc == nil || !proc.Alive() {
		t.Fatal("already-active web.service must keep running")
	}
	if lateProc != nil && lateProc.Alive() {
		t.Fatal("late.service must not be running")
	}
}

type gatedLauncher struct {
	fakeLauncher
	mu           sync.Mutex
	failUnit     string
	probeOnce    sync.Once
	probeStarted chan struct{}
}

func newGatedLauncher() *gatedLauncher {
	return &gatedLauncher{probeStarted: make(chan struct{})}
}

func (g *gatedLauncher) failAfterProbe(name string) {
	g.mu.Lock()
	g.failUnit = name
	g.mu.Unlock()
}

func (g *gatedLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	g.mu.Lock()
	fail := g.failUnit
	g.mu.Unlock()
	if spec.Unit == "probe.service" {
		g.probeOnce.Do(func() { close(g.probeStarted) })
	}
	if spec.Unit == fail {
		select {
		case <-g.probeStarted:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
			return nil, errors.New("timeout waiting for probe")
		}
		return nil, errors.New("start failed")
	}
	if spec.Unit == "late.service" {
		return nil, errors.New("late.service must not start")
	}
	return g.fakeLauncher.Start(ctx, spec)
}

var _ runtime.Launcher = (*gatedLauncher)(nil)
