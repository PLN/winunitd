package manager

import (
	"context"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
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

func TestStopTargetStopsWantsInReverse(t *testing.T) {
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
	got := launch.stopped()
	if len(got) < 2 || got[0] != "web.service" || got[1] != "db.service" {
		t.Fatalf("stop order = %v, want web then db", got)
	}
	assertState(t, m, "app.target", core.Inactive)
	assertState(t, m, "web.service", core.Inactive)
	assertState(t, m, "db.service", core.Inactive)
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
	m := managerWithClock(t, launch, timers.Clock{
		Now:       time.Now,
		SinceBoot: func() time.Duration { return time.Hour },
		Startup:   time.Now(),
	}, map[string]string{
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
	time.Sleep(80 * time.Millisecond)
	if containsString(launch.units(), "job.service") {
		t.Fatal("disarmed timer must not activate the service")
	}
}

func TestShutdownCancelsPendingRestart(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(2)}
	m := managerWith(t, launch, map[string]string{
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
	waitStarts(t, launch, 1, 2*time.Second)
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	n := launch.nstarts()
	time.Sleep(200 * time.Millisecond)
	if got := launch.nstarts(); got != n {
		t.Fatalf("restart after shutdown: starts %d -> %d", n, got)
	}
}
