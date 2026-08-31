package manager

import (
	"context"
	"os"
	"path/filepath"
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
	m := managerWith(t, launch, map[string]string{
		"job.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\job.exe
WorkingDirectory=C:\Tools
`,
		"job.timer": `
[Timer]
OnStartupSec=50ms
`,
	})
	if _, err := m.Start(context.Background(), "job.timer"); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "job.timer", core.Active)
	waitLauncherUnit(t, launch, "job.service", 2*time.Second)
	assertState(t, m, "job.service", core.Active)

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
	now := time.Now()
	m := managerWithClock(t, launch, timers.Clock{
		Now:       time.Now,
		SinceBoot: func() time.Duration { return time.Hour },
		Startup:   now,
	}, map[string]string{
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
	waitLauncherUnit(t, launch, "boot.service", 2*time.Second)
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		if containsString(launch.units(), "start.service") {
			t.Fatal("OnStartupSec must not fire as early as OnBootSec after a long machine uptime")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBootStartsEnabledTimer(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	m := managerWith(t, launch, map[string]string{
		"backup.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\backup.exe
WorkingDirectory=C:\Tools
`,
		"backup.timer": `
[Timer]
OnStartupSec=40ms
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
OnStartupSec=40ms
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
	waitLauncherUnit(t, launch, "backup.service", 2*time.Second)
	if containsString(launch.units(), "idle.service") {
		t.Fatal("disabled timer must not activate its service")
	}
}

func TestStopTimerDoesNotFire(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	m := managerWith(t, launch, map[string]string{
		"foo.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.timer": `
[Timer]
OnStartupSec=80ms
`,
	})
	if _, err := m.Start(context.Background(), "foo.timer"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stop("foo.timer"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond)
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
	if err := store.Save("backup.timer", timers.Runtime{LastActual: time.Now().Add(-48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	m, err := New(Config{BaseDir: dir, Launch: launch})
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
	waitLauncherUnit(t, launch, "backup.service", 2*time.Second)
}

func TestListTimersNextLastOverPipe(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	m := managerWith(t, launch, map[string]string{
		"foo.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.timer": `
[Timer]
OnStartupSec=40ms
`,
	})
	client, stop := serveManager(t, m, protocol.AllowAdmin)
	defer stop()
	if _, err := client.Start(context.Background(), "foo.timer"); err != nil {
		t.Fatal(err)
	}
	waitLauncherUnit(t, launch, "foo.service", 2*time.Second)
	got, err := client.ListTimers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Timers) != 1 || got.Timers[0].Last == "" {
		t.Fatalf("timers = %+v", got.Timers)
	}
}

func managerWithClock(t *testing.T, launch *fakeLauncher, clk timers.Clock, files map[string]string) *Manager {
	t.Helper()
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		writeUnit(t, units, name, body)
	}
	m, err := New(Config{BaseDir: dir, Launch: launch, Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	return m
}

func TestOnUnitActiveSecRepeatsAfterActivation(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(0)}
	m := managerWith(t, launch, map[string]string{
		"foo.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.timer": `
[Timer]
OnUnitActiveSec=80ms
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
	const interval = 80 * time.Millisecond
	waitUntil(t, 2*time.Second, func() bool { return launch.nstarts() >= 2 })
	if n := launch.nstarts(); n != 2 {
		t.Fatalf("starts = %d, want 2", n)
	}
	at := launch.startTimes()
	if gap := at[1].Sub(at[0]); gap < interval {
		t.Fatalf("gap = %s, want >= OnUnitActiveSec (%s)", gap, interval)
	}
	time.Sleep(interval / 2)
	if n := launch.nstarts(); n != 2 {
		t.Fatalf("starts = %d after half interval, want 2", n)
	}
}

func waitLauncherUnit(t *testing.T, launch *fakeLauncher, name string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if containsString(launch.units(), name) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("launcher never started %s; started %v", name, launch.units())
}
