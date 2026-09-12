package manager

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/timers"
	"github.com/PLN/winunitd/internal/unit"
)

func TestStoppedTimerCannotLaunchQueuedCompanion(t *testing.T) {
	launch := &fakeLauncher{}
	m, _ := managerWithFake(t, launch, map[string]string{
		"input.timer":   "[Timer]\nOnStartupSec=1h\n",
		"input.service": "[Service]\nExecStart=C:\\Tools\\input.exe\n",
	})
	if _, err := m.Start(context.Background(), "input.timer"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	u := m.units["input.timer"].unit
	source := timers.Fire{Name: u.Name, Unit: u.Timer.Unit, Token: m.engine.Arm(timerSpec(u))}
	m.mu.Unlock()
	waitCond(t, func() bool { return m.engine.Current(source.Name, source.Token) })
	unlock := m.ops.lock("input.service")
	var once sync.Once
	release := func() { once.Do(unlock) }
	defer release()
	done := make(chan struct{})
	go func() { m.onTimerElapsed(source); close(done) }()
	waitCond(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.units["input.service"].operations > 0 })
	if _, err := m.Stop("input.timer"); err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("queued callback did not finish")
	}
	if len(launch.specs()) != 0 {
		t.Fatal("stopped source launched a queued companion")
	}
	assertState(t, m, "input.service", core.Inactive)
}

func TestStoppedTimerPreservesAlreadyLaunchedCompanion(t *testing.T) {
	launch := newGatedStartLauncher("input.service")
	m, _ := managerWithFake(t, launch, map[string]string{
		"input.timer":   "[Timer]\nOnStartupSec=1h\n",
		"input.service": "[Service]\nExecStart=C:\\Tools\\input.exe\n",
	})
	var once sync.Once
	release := func() { once.Do(launch.release) }
	defer release()
	if _, err := m.Start(context.Background(), "input.timer"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	u := m.units["input.timer"].unit
	source := timers.Fire{Name: u.Name, Unit: u.Timer.Unit, Token: m.engine.Arm(timerSpec(u))}
	m.mu.Unlock()
	waitCond(t, func() bool { return m.engine.Current(source.Name, source.Token) })
	done := make(chan struct{})
	go func() { m.onTimerElapsed(source); close(done) }()
	select {
	case <-launch.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("companion did not reach launcher")
	}
	if _, err := m.Stop("input.timer"); err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("companion launch did not finish")
	}
	assertState(t, m, "input.service", core.Active)
	if len(launch.specs()) != 1 {
		t.Fatal("admitted companion was not launched")
	}
	if _, err := m.Stop("input.service"); err != nil {
		t.Fatal(err)
	}
}

func TestReloadKeepsArmedTimerDefinition(t *testing.T) {
	launch := &fakeLauncher{}
	m, clock := managerWithFake(t, launch, map[string]string{
		"work.timer":  "[Timer]\nUnit=old.service\nOnStartupSec=10s\n",
		"old.service": "[Service]\nExecStart=C:\\Tools\\old.exe\n",
		"new.service": "[Service]\nExecStart=C:\\Tools\\new.exe\n",
	})
	if _, err := m.Start(context.Background(), "work.timer"); err != nil {
		t.Fatal(err)
	}
	before, _ := m.Status("work.timer")
	armedRevision := before.Unit.ArmedConfigRevision
	if armedRevision == "" || armedRevision != before.Unit.ConfigRevision {
		t.Fatal("timer arm did not capture configuration revision")
	}
	if err := os.WriteFile(filepath.Join(m.cfg.UnitsDir(), "work.timer"), []byte("[Timer]\nUnit=new.service\nOnBootSec=1h20s\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	after, _ := m.Status("work.timer")
	if after.Unit.ArmedConfigRevision != armedRevision || after.Unit.ConfigRevision == armedRevision {
		t.Fatal("reload relabelled the armed timer")
	}
	listed, err := m.ListTimers()
	if err != nil || len(listed.Timers) != 1 || listed.Timers[0].Unit != "old.service" {
		t.Fatal("list-timers reported a companion other than the armed target")
	}
	clock.Advance(11 * time.Second)
	m.engine.ClockChanged()
	waitState(t, m, "old.service", core.Active)
	assertState(t, m, "new.service", core.Inactive)
	if _, err := m.Stop("work.timer"); err != nil {
		t.Fatal(err)
	}
	stopped, _ := m.Status("work.timer")
	if stopped.Unit.ArmedConfigRevision != "" {
		t.Fatal("disarmed timer retained an armed revision")
	}
	if _, err := m.Start(context.Background(), "work.timer"); err != nil {
		t.Fatal(err)
	}
	rearmed, _ := m.Status("work.timer")
	if rearmed.Unit.ArmedConfigRevision != rearmed.Unit.ConfigRevision || rearmed.Unit.ArmedConfigRevision == armedRevision {
		t.Fatal("fresh timer arm did not capture the accepted revision")
	}
	clock.Advance(10 * time.Second)
	m.engine.ClockChanged()
	waitState(t, m, "new.service", core.Active)
}

// Keep the fixture process alive; the manager still uses the oneshot definition
// and owns its startup wait. The base fake otherwise exits oneshots immediately.
type pendingTimerOneshotLauncher struct{ fakeLauncher }

func (l *pendingTimerOneshotLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	spec.Type = unit.TypeSimple
	return l.fakeLauncher.Start(ctx, spec)
}

func TestShutdownCancelsTimerOneshotWait(t *testing.T) {
	launch := &pendingTimerOneshotLauncher{}
	m, clock := managerWithFake(t, launch, map[string]string{
		"work.timer":   "[Timer]\nOnStartupSec=1s\n",
		"work.service": "[Service]\nType=oneshot\nExecStart=C:\\Tools\\work.exe\nTimeoutStartSec=1h\n",
	})
	// Also release the startup wait if the shutdown regression times out.
	defer m.Close()
	if _, err := m.Start(context.Background(), "work.timer"); err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Second)
	m.engine.ClockChanged()
	waitCond(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		rt := m.units["work.service"]
		return rt.proc != nil && rt.startCancel != nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown did not cancel and drain the timer workload: %v", err)
	}
	m.mu.Lock()
	owned := m.units["work.service"].proc
	m.mu.Unlock()
	if owned != nil {
		t.Fatal("shutdown left the timer workload owned")
	}
}
