package manager

import (
	"context"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/timers"
)

func TestStartLimitBurstStopsRelaunch(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(2), holdAutoExit: true}
	m, fk := managerWithFake(t, launch, map[string]string{
		"foo.service": `
[Unit]
StartLimitIntervalSec=10s
StartLimitBurst=2
[Service]
Type=simple
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
Restart=on-failure
RestartSec=1s
`,
	})
	crashToStartLimit(t, m, fk, launch, "foo.service", 2, time.Second)
	assertState(t, m, "foo.service", core.Failed)
	assertReason(t, m, "foo.service", core.ReasonStartLimit)
	n := launch.nstarts()
	if n != 2 {
		t.Fatalf("starts = %d, want 2", n)
	}
	fk.Advance(time.Second)
	if got := launch.nstarts(); got != n {
		t.Fatalf("relaunched after start-limit: starts %d -> %d", n, got)
	}
	assertState(t, m, "foo.service", core.Failed)
	assertReason(t, m, "foo.service", core.ReasonStartLimit)
}

func TestStartLimitExplicitStartResets(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(2), holdAutoExit: true}
	m, fk := managerWithFake(t, launch, map[string]string{
		"foo.service": `
[Unit]
StartLimitIntervalSec=10s
StartLimitBurst=2
[Service]
Type=simple
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
Restart=on-failure
RestartSec=1s
`,
	})
	crashToStartLimit(t, m, fk, launch, "foo.service", 2, time.Second)
	if _, err := m.Start(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, "foo.service", core.Active)
	if got := launch.nstarts(); got != 3 {
		t.Fatalf("explicit start after start-limit: starts = %d, want 3", got)
	}
	st, err := m.Status("foo.service")
	if err != nil {
		t.Fatal(err)
	}
	if st.Unit == nil || st.Unit.Reason == core.ReasonStartLimit {
		t.Fatalf("explicit start must clear start-limit reason: %+v", st.Unit)
	}

	launch.releaseExits()
	waitSub(t, m, "foo.service", core.SubAutoRestart)
	advanceArmed(t, fk, time.Second)
	waitState(t, m, "foo.service", core.Active)
	waitCond(t, func() bool { return launch.nstarts() >= 4 })
	launch.releaseExits()
	waitState(t, m, "foo.service", core.Failed)
	assertReason(t, m, "foo.service", core.ReasonStartLimit)
	if got := launch.nstarts(); got != 4 {
		t.Fatalf("starts after second hit = %d, want 4", got)
	}
	fk.Advance(time.Second)
	if got := launch.nstarts(); got != 4 {
		t.Fatalf("relaunched after second start-limit: %d", got)
	}
}

func TestStartLimitBurstZeroNeverHits(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(2), holdAutoExit: true}
	m, fk := managerWithFake(t, launch, map[string]string{
		"foo.service": `
[Unit]
StartLimitIntervalSec=10s
StartLimitBurst=0
[Service]
Type=simple
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
Restart=on-failure
RestartSec=1s
`,
	})
	if _, err := m.Start(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, "foo.service", core.Active)
	for i := 2; i <= 6; i++ {
		launch.releaseExits()
		waitSub(t, m, "foo.service", core.SubAutoRestart)
		advanceArmed(t, fk, time.Second)
		waitState(t, m, "foo.service", core.Active)
		waitCond(t, func() bool { return launch.nstarts() >= i })
	}
	assertState(t, m, "foo.service", core.Active)
	st, err := m.Status("foo.service")
	if err != nil {
		t.Fatal(err)
	}
	if st.Unit != nil && st.Unit.Reason == core.ReasonStartLimit {
		t.Fatal("StartLimitBurst=0 must not fail with start-limit")
	}
	if got := launch.nstarts(); got != 6 {
		t.Fatalf("starts = %d, want 6", got)
	}
}

func TestStartLimitDefaultBurstFive(t *testing.T) {
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
	crashToStartLimit(t, m, fk, launch, "foo.service", 5, time.Second)
	if got := launch.nstarts(); got != 5 {
		t.Fatalf("starts = %d, want 5", got)
	}
	assertReason(t, m, "foo.service", core.ReasonStartLimit)
	fk.Advance(time.Second)
	if got := launch.nstarts(); got != 5 {
		t.Fatalf("relaunched after default start-limit: %d", got)
	}
}

func TestStartLimitWindowUsesFakeClock(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(2), holdAutoExit: true}
	m, fk := managerWithFake(t, launch, map[string]string{
		"foo.service": `
[Unit]
StartLimitIntervalSec=5s
StartLimitBurst=2
[Service]
Type=simple
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
Restart=on-failure
RestartSec=6s
`,
	})
	if _, err := m.Start(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, "foo.service", core.Active)
	launch.releaseExits()
	waitSub(t, m, "foo.service", core.SubAutoRestart)
	advanceArmed(t, fk, 6*time.Second)
	waitState(t, m, "foo.service", core.Active)
	launch.releaseExits()
	waitSub(t, m, "foo.service", core.SubAutoRestart)
	if got := launch.nstarts(); got != 2 {
		t.Fatalf("starts = %d, want 2", got)
	}
	st, err := m.Status("foo.service")
	if err != nil {
		t.Fatal(err)
	}
	if st.Unit != nil && st.Unit.Reason == core.ReasonStartLimit {
		t.Fatal("starts spaced beyond Interval must not hit the limit")
	}
}

func crashToStartLimit(t *testing.T, m *Manager, fk *timers.Fake, launch *scriptedLauncher, name string, burst int, delay time.Duration) {
	t.Helper()
	if _, err := m.Start(context.Background(), name); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, name, core.Active)
	for i := 1; i < burst; i++ {
		launch.releaseExits()
		waitSub(t, m, name, core.SubAutoRestart)
		advanceArmed(t, fk, delay)
		waitState(t, m, name, core.Active)
	}
	launch.releaseExits()
	waitState(t, m, name, core.Failed)
	assertReason(t, m, name, core.ReasonStartLimit)
}

func assertReason(t *testing.T, m *Manager, name, want string) {
	t.Helper()
	st, err := m.Status(name)
	if err != nil {
		t.Fatal(err)
	}
	if st.Unit == nil {
		t.Fatal("no unit status")
	}
	if st.Unit.Reason != want {
		t.Fatalf("reason = %q error=%q state=%s, want %s", st.Unit.Reason, st.Unit.Error, st.Unit.ActiveState, want)
	}
}
