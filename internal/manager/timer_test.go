package manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/timers"
)

func TestBuiltinDefaultTargetWantsTimers(t *testing.T) {
	t.Parallel()
	m := testManager(t, nil)
	m.mu.Lock()
	defer m.mu.Unlock()
	if !containsString(m.graph.Wants(DefaultTarget), TimersTarget) {
		t.Fatalf("default.target wants = %v", m.graph.Wants(DefaultTarget))
	}
	if !containsString(m.graph.After(DefaultTarget), TimersTarget) {
		t.Fatalf("default.target after = %v", m.graph.After(DefaultTarget))
	}
}

func TestOnStartupSecActivatesMatchingService(t *testing.T) {
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
	assertState(t, m, "job.timer", core.Active)
	advanceWait(t, fk, 5*time.Second)
	waitLauncherUnit(t, launch, "job.service")
	waitState(t, m, "job.service", core.Active)

	st, err := m.Status("job.timer")
	if err != nil || st.Unit == nil || st.Unit.Last == "" {
		t.Fatalf("status last = %+v err=%v", st, err)
	}
	list, err := m.ListTimers()
	if err != nil || len(list.Timers) != 1 || list.Timers[0].Last == "" || list.Timers[0].Unit != "job.service" {
		t.Fatalf("list-timers = %+v err=%v", list, err)
	}
}

func TestOnBootSecVsOnStartupSec(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	fk := timers.NewFake(time.Time{})
	m := managerWithClock(t, launch, fk.Clock(), map[string]string{
		"boot.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\boot.exe
WorkingDirectory=C:\Tools
`,
		"boot.timer": `
[Timer]
OnBootSec=10ms
`,
		"start.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\start.exe
WorkingDirectory=C:\Tools
`,
		"start.timer": `
[Timer]
OnStartupSec=10s
`,
	})
	if _, err := m.Start(context.Background(), "boot.timer"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(context.Background(), "start.timer"); err != nil {
		t.Fatal(err)
	}
	waitLauncherUnit(t, launch, "boot.service")
	advanceWait(t, fk, 400*time.Millisecond)
	if containsString(launch.units(), "start.service") {
		t.Fatal("OnStartupSec must not fire as early as OnBootSec after a long machine uptime")
	}
}

func TestBootStartsEnabledTimer(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	m, fk := managerWithFake(t, launch, map[string]string{
		"backup.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\backup.exe
WorkingDirectory=C:\Tools
`,
		"backup.timer": `
[Timer]
OnStartupSec=5s
[Install]
WantedBy=timers.target
`,
		"idle.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\idle.exe
WorkingDirectory=C:\Tools
`,
		"idle.timer": `
[Timer]
OnStartupSec=5s
[Install]
WantedBy=timers.target
`,
	})
	if _, err := m.Enable("backup.timer"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, DefaultTarget, core.Active)
	assertState(t, m, TimersTarget, core.Active)
	assertState(t, m, "backup.timer", core.Active)
	assertState(t, m, "idle.timer", core.Inactive)
	advanceWait(t, fk, 5*time.Second)
	waitLauncherUnit(t, launch, "backup.service")
	if containsString(launch.units(), "idle.service") {
		t.Fatal("disabled timer must not activate its service")
	}
}

func TestStopTimerDoesNotFire(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	m, fk := managerWithFake(t, launch, map[string]string{
		"foo.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.timer": `
[Timer]
OnStartupSec=5s
`,
	})
	if _, err := m.Start(context.Background(), "foo.timer"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stop("foo.timer"); err != nil {
		t.Fatal(err)
	}
	fk.Advance(5 * time.Second)
	if containsString(launch.units(), "foo.service") {
		t.Fatal("stopped timer must not activate the service")
	}
}

func TestPersistentTimerCatchup(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, units, "backup.service", `
[Service]
Type=oneshot
ExecStart=C:\Tools\backup.exe
WorkingDirectory=C:\Tools
`)
	writeUnit(t, units, "backup.timer", `
[Timer]
OnCalendar=daily
Persistent=yes
`)
	stateDir := filepath.Join(dir, "runtime", "timers")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := timers.OpenStore(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	fk := timers.NewFake(time.Time{})
	if err := store.Save("backup.timer", timers.Runtime{LastActual: fk.Now().Add(-48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	m, err := New(Config{BaseDir: dir, Launch: launch, Clock: fk.Clock()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	if _, err := m.Start(context.Background(), "backup.timer"); err != nil {
		t.Fatal(err)
	}
	waitLauncherUnit(t, launch, "backup.service")
}

func TestListTimersNextLastOverPipe(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	m, fk := managerWithFake(t, launch, map[string]string{
		"foo.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.timer": `
[Timer]
OnStartupSec=5s
`,
	})
	client, stop := serveManager(t, m, protocol.AllowAdmin)
	defer stop()
	if _, err := client.Start(context.Background(), "foo.timer"); err != nil {
		t.Fatal(err)
	}
	advanceWait(t, fk, 5*time.Second)
	waitLauncherUnit(t, launch, "foo.service")
	got, err := client.ListTimers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Timers) != 1 || got.Timers[0].Last == "" {
		t.Fatalf("timers = %+v", got.Timers)
	}
}

func TestOnUnitActiveSecRepeatsAfterActivation(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(0)}
	m, fk := managerWithFake(t, launch, map[string]string{
		"foo.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.timer": `
[Timer]
OnUnitActiveSec=5s
`,
	})
	if _, err := m.Start(context.Background(), "foo.timer"); err != nil {
		t.Fatal(err)
	}
	if launch.nstarts() != 0 {
		t.Fatalf("timer must not start the service before the unit has been active: %d", launch.nstarts())
	}
	if _, err := m.Start(context.Background(), "foo.service"); err != nil {
		t.Fatal(err)
	}
	advanceWait(t, fk, 5*time.Second)
	waitCond(t, func() bool { return launch.nstarts() >= 2 })
	if n := launch.nstarts(); n != 2 {
		t.Fatalf("starts = %d, want 2", n)
	}
	fk.Advance(2 * time.Second)
	if n := launch.nstarts(); n != 2 {
		t.Fatalf("starts = %d after half interval, want 2", n)
	}
}

func waitLauncherUnit(t *testing.T, launch *fakeLauncher, name string) {
	t.Helper()
	waitCond(t, func() bool { return containsString(launch.units(), name) })
}

func TestStatusConcurrentWithTimerTick(t *testing.T) {
	// Not Parallel: a Status/ListTimers storm on Windows CI starved
	// TestRestartOnWatchdogRelaunches (8a193aa).
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
	waitCond(t, func() bool {
		st, err := m.Status("job.timer")
		return err == nil && st.Unit != nil && st.Unit.Next != ""
	})

	stop := make(chan struct{})
	var statusErr atomic.Value
	go func() {
		// Do not busy-spin: a tight Status loop starves other Parallel
		// tests on Windows CI (same class as the Gosched wait in
		// waitErr / issue #92).
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			st, err := m.Status("job.timer")
			if err != nil {
				statusErr.Store(err)
				return
			}
			if st.Unit == nil {
				statusErr.Store(fmt.Errorf("status missing unit"))
				return
			}
			_, err = m.ListTimers()
			if err != nil {
				statusErr.Store(err)
				return
			}
		}
	}()

	advanceWait(t, fk, 5*time.Second)
	waitLauncherUnit(t, launch, "job.service")
	close(stop)

	st, err := m.Status("job.timer")
	if err != nil || st.Unit == nil || st.Unit.Last == "" {
		t.Fatalf("status after tick = %+v err=%v", st, err)
	}
	if v := statusErr.Load(); v != nil {
		t.Fatal(v)
	}
}

func TestListTimersNextAfterCalendarJump(t *testing.T) {
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
OnCalendar=*-*-* 15:00:00
`,
	})
	if _, err := m.Start(context.Background(), "job.timer"); err != nil {
		t.Fatal(err)
	}
	waitCond(t, func() bool {
		st, err := m.Status("job.timer")
		return err == nil && st.Unit != nil && st.Unit.Next == "2026-09-01T15:00:00Z"
	})
	fk.JumpWall(fk.Now().Add(4 * time.Hour))
	m.ClockChanged()
	waitLauncherUnit(t, launch, "job.service")
	st, err := m.Status("job.timer")
	if err != nil || st.Unit == nil || st.Unit.Next != "2026-09-02T15:00:00Z" {
		t.Fatalf("status next after jump = %+v err=%v", st, err)
	}
	list, err := m.ListTimers()
	if err != nil || len(list.Timers) != 1 || list.Timers[0].Next != "2026-09-02T15:00:00Z" {
		t.Fatalf("list-timers next after jump = %+v err=%v", list, err)
	}
}
