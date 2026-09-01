package manager

import (
	"context"
	"os"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/notify"
	"github.com/PLN/winunitd/internal/unit"
)

// fakeNotifyLaunch reports this test process as the unit main PID.
//
// CI #42 (windows job on fc55a5a): portable tests Dial'd the real notify
// listener (named pipe on Windows, TCP on Linux) and SendRetry returned
// nil, but Start still hit TimeoutStartSec waiting for READY=1.
// internal/notify named-pipe Accept/Dial passed; TestWindowsNotify* (READY
// from the helper, which is the real main PID) were not in the fail list.
// NotifyAccess=main compared GetNamedPipeClientProcessId (this process) to
// fakeLauncher's default PID 1 and dropped the payload. Linux TCP reports
// ClientPID 0 and skips that check.
func fakeNotifyLaunch() *fakeLauncher {
	return &fakeLauncher{pid: os.Getpid()}
}

func TestNotifyStaysActivatingUntilReady(t *testing.T) {
	t.Parallel()
	launch := fakeNotifyLaunch()
	m := managerWith(t, launch, map[string]string{
		"worker.service": `
[Service]
Type=notify
ExecStart=C:\App\worker.exe
WorkingDirectory=C:\App
TimeoutStartSec=5s
`,
	})

	errc := make(chan error, 1)
	go func() {
		_, err := m.Start(context.Background(), "worker")
		errc <- err
	}()

	pipe := waitNotifyPipe(t, launch, "worker.service")
	waitUntil(t, 2*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.stateOfLocked("worker.service") == core.Activating
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := notify.SendRetry(ctx, pipe, notify.Message{Ready: true}); err != nil {
		t.Fatal(err)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "worker.service", core.Active)
}

func TestNotifyTimeoutStartWithoutReadyFails(t *testing.T) {
	t.Parallel()
	launch := fakeNotifyLaunch()
	m, fk := managerWithFake(t, launch, map[string]string{
		"late.service": `
[Service]
Type=notify
ExecStart=C:\App\late.exe
WorkingDirectory=C:\App
TimeoutStartSec=5s
`,
	})
	errc := make(chan error, 1)
	go func() {
		_, err := m.Start(context.Background(), "late")
		errc <- err
	}()
	waitCond(t, func() bool { return len(launch.specs()) >= 1 })
	advanceWait(t, fk, 5*time.Second)
	err := waitErr(t, errc)
	if err == nil {
		t.Fatal("expected TimeoutStartSec failure")
	}
	if !strings.Contains(err.Error(), "READY=1") && !strings.Contains(err.Error(), "TimeoutStartSec") {
		t.Fatalf("err = %v", err)
	}
	assertState(t, m, "late.service", core.Failed)
}

func TestWatchdogPulseRefreshesTimer(t *testing.T) {
	t.Parallel()
	launch := fakeNotifyLaunch()
	m, fk := managerWithFake(t, launch, map[string]string{
		"hb.service": `
[Service]
Type=notify
ExecStart=C:\App\hb.exe
WorkingDirectory=C:\App
TimeoutStartSec=5s
WatchdogSec=1s
`,
	})
	errc := make(chan error, 1)
	go func() {
		_, err := m.Start(context.Background(), "hb")
		errc <- err
	}()
	pipe := waitNotifyPipe(t, launch, "hb.service")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := notify.SendRetry(ctx, pipe, notify.Message{Ready: true}); err != nil {
		t.Fatal(err)
	}
	if err := waitErr(t, errc); err != nil {
		t.Fatal(err)
	}

	advanceWait(t, fk, 400*time.Millisecond)
	assertState(t, m, "hb.service", core.Active)
	if err := notify.Send(ctx, pipe, notify.Message{Watchdog: true}); err != nil {
		t.Fatal(err)
	}
	waitCond(t, func() bool {
		when, ok := fk.NextWhen()
		return ok && !when.Before(fk.Now().Add(900*time.Millisecond))
	})
	advanceWait(t, fk, time.Second)
	waitState(t, m, "hb.service", core.Failed)
}

func TestNotifyReadyLeavesOnlyWatchdogTimer(t *testing.T) {
	t.Parallel()
	launch := fakeNotifyLaunch()
	m, fk := managerWithFake(t, launch, map[string]string{
		"onlywd.service": `
[Service]
Type=notify
ExecStart=C:\App\onlywd.exe
WorkingDirectory=C:\App
TimeoutStartSec=5s
WatchdogSec=1s
Restart=no
`,
	})
	errc := make(chan error, 1)
	go func() {
		_, err := m.Start(context.Background(), "onlywd")
		errc <- err
	}()
	pipe := waitNotifyPipe(t, launch, "onlywd.service")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := notify.SendRetry(ctx, pipe, notify.Message{Ready: true}); err != nil {
		t.Fatal(err)
	}
	if err := waitErr(t, errc); err != nil {
		t.Fatal(err)
	}
	waitCond(t, func() bool {
		_, ok := fk.NextWhen()
		return ok
	})
	when, _ := fk.NextWhen()
	if remain := when.Sub(fk.Now()); remain > time.Second+time.Millisecond {
		t.Fatalf("pending wait %v, want WatchdogSec=1s", remain)
	}
}

func TestMissedWatchdogSecFailsUnit(t *testing.T) {
	t.Parallel()
	launch := fakeNotifyLaunch()
	m, fk := managerWithFake(t, launch, map[string]string{
		"miss.service": `
[Service]
Type=notify
ExecStart=C:\App\miss.exe
WorkingDirectory=C:\App
TimeoutStartSec=5s
WatchdogSec=1s
Restart=no
`,
	})
	errc := make(chan error, 1)
	go func() {
		_, err := m.Start(context.Background(), "miss")
		errc <- err
	}()
	pipe := waitNotifyPipe(t, launch, "miss.service")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := notify.SendRetry(ctx, pipe, notify.Message{Ready: true}); err != nil {
		t.Fatal(err)
	}
	if err := waitErr(t, errc); err != nil {
		t.Fatal(err)
	}
	advanceWait(t, fk, time.Second)
	waitCond(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.stateOfLocked("miss.service") == core.Failed && m.subOfLocked("miss.service") == core.SubWatchdog
	})
}

func TestWatchdogExactlyAtBoundary(t *testing.T) {
	t.Parallel()
	launch := fakeNotifyLaunch()
	m, fk := managerWithFake(t, launch, map[string]string{
		"edge.service": `
[Service]
Type=notify
ExecStart=C:\App\edge.exe
WorkingDirectory=C:\App
TimeoutStartSec=5s
WatchdogSec=1s
Restart=no
`,
	})
	errc := make(chan error, 1)
	go func() {
		_, err := m.Start(context.Background(), "edge")
		errc <- err
	}()
	pipe := waitNotifyPipe(t, launch, "edge.service")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := notify.SendRetry(ctx, pipe, notify.Message{Ready: true}); err != nil {
		t.Fatal(err)
	}
	if err := waitErr(t, errc); err != nil {
		t.Fatal(err)
	}
	advanceWait(t, fk, time.Second-time.Nanosecond)
	assertState(t, m, "edge.service", core.Active)
	advanceWait(t, fk, time.Nanosecond)
	waitCond(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.stateOfLocked("edge.service") == core.Failed && m.subOfLocked("edge.service") == core.SubWatchdog
	})
}

func TestRestartOnWatchdogRelaunches(t *testing.T) {
	t.Parallel()
	launch := fakeNotifyLaunch()
	m, fk := managerWithFake(t, launch, map[string]string{
		"wd.service": `
[Service]
Type=notify
ExecStart=C:\App\wd.exe
WorkingDirectory=C:\App
TimeoutStartSec=5s
WatchdogSec=1s
Restart=on-watchdog
RestartSec=2s
`,
	})
	go keepReady(t, launch, "wd.service")
	errc := make(chan error, 1)
	go func() {
		_, err := m.Start(context.Background(), "wd")
		errc <- err
	}()
	waitNotifyPipe(t, launch, "wd.service")
	if err := waitErr(t, errc); err != nil {
		t.Fatal(err)
	}
	advanceWait(t, fk, time.Second)
	waitSub(t, m, "wd.service", core.SubAutoRestart)
	advanceArmed(t, fk, 2*time.Second)
	waitCond(t, func() bool { return len(launch.specs()) >= 2 })
}

func TestNotifyInjectsEnv(t *testing.T) {
	t.Parallel()
	launch := fakeNotifyLaunch()
	m := managerWith(t, launch, map[string]string{
		"env.service": `
[Service]
Type=notify
ExecStart=C:\App\env.exe
WorkingDirectory=C:\App
TimeoutStartSec=5s
WatchdogSec=30s
`,
	})
	errc := make(chan error, 1)
	go func() {
		_, err := m.Start(context.Background(), "env")
		errc <- err
	}()
	pipe := waitNotifyPipe(t, launch, "env.service")
	if pipe == "" {
		t.Fatal("WINUNIT_NOTIFY_PIPE missing")
	}
	if goruntime.GOOS == "windows" {
		prefix := `\\.\pipe\winunitd\notify\`
		if !strings.HasPrefix(pipe, prefix) {
			t.Fatalf("Windows notify pipe = %q, want prefix %s", pipe, prefix)
		}
	} else if strings.HasPrefix(pipe, `\\.\pipe\`) {
		t.Fatalf("Linux fake listener must not be a Windows pipe name: %q", pipe)
	}
	usec, ok := notify.LookupEnv(launch.specs()[0].Env, notify.EnvWatchdogUsec)
	if !ok || usec != "30000000" {
		t.Fatalf("WINUNIT_WATCHDOG_USEC = %q ok=%v", usec, ok)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := notify.SendRetry(ctx, pipe, notify.Message{Ready: true}); err != nil {
		t.Fatal(err)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}

func TestSimpleDoesNotInjectNotifyPipe(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	m := managerWith(t, launch, map[string]string{
		"plain.service": `
[Service]
Type=simple
ExecStart=C:\Tools\plain.exe
WorkingDirectory=C:\Tools
`,
	})
	if _, err := m.Start(context.Background(), "plain"); err != nil {
		t.Fatal(err)
	}
	got := launch.specs()
	if len(got) != 1 {
		t.Fatalf("specs = %+v", got)
	}
	if _, ok := notify.LookupEnv(got[0].Env, notify.EnvNotifyPipe); ok {
		t.Fatal("simple without WatchdogSec must not inject WINUNIT_NOTIFY_PIPE")
	}
	if got[0].Type != unit.TypeSimple {
		t.Fatalf("type = %s", got[0].Type)
	}
}

func TestWinunitNotifyCLIAgainstLiveManager(t *testing.T) {
	t.Parallel()
	launch := fakeNotifyLaunch()
	m := managerWith(t, launch, map[string]string{
		"cli.service": `
[Service]
Type=notify
ExecStart=C:\App\cli.exe
WorkingDirectory=C:\App
TimeoutStartSec=5s
WatchdogSec=2s
`,
	})
	errc := make(chan error, 1)
	go func() {
		_, err := m.Start(context.Background(), "cli")
		errc <- err
	}()
	pipe := waitNotifyPipe(t, launch, "cli.service")

	getenv := func(k string) string {
		if k == notify.EnvNotifyPipe {
			return pipe
		}
		return ""
	}
	// Inline the CLI send path used by winunit-notify.exe.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := notify.Send(ctx, getenv(notify.EnvNotifyPipe), notify.Message{Ready: true}); err != nil {
		t.Fatal(err)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if err := notify.Send(ctx, pipe, notify.Message{Watchdog: true}); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "cli.service", core.Active)
}

func waitNotifyPipe(t *testing.T, launch *fakeLauncher, unit string) string {
	t.Helper()
	var pipe string
	waitCond(t, func() bool {
		for _, spec := range launch.specs() {
			if spec.Unit != unit {
				continue
			}
			v, ok := notify.LookupEnv(spec.Env, notify.EnvNotifyPipe)
			if ok && v != "" {
				pipe = v
				return true
			}
		}
		return false
	})
	return pipe
}

func keepReady(t *testing.T, launch *fakeLauncher, unit string) {
	t.Helper()
	// Windows named-pipe addresses are the unit name, so two launches share
	// the same WINUNIT_NOTIFY_PIPE string. Send READY once per Start, not
	// once per distinct address.
	sent := 0
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && sent < 2 {
		specs := launch.specs()
		if sent >= len(specs) {
			goruntime.Gosched()
			continue
		}
		spec := specs[sent]
		if spec.Unit != unit {
			goruntime.Gosched()
			continue
		}
		pipe, ok := notify.LookupEnv(spec.Env, notify.EnvNotifyPipe)
		if !ok || pipe == "" {
			goruntime.Gosched()
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = notify.SendRetry(ctx, pipe, notify.Message{Ready: true})
		cancel()
		sent++
	}
}
