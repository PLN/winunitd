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
	"github.com/PLN/winunitd/internal/timers"
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

	if err := sendReadyUntilStart(t, pipe, errc); err != nil {
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
TimeoutStartSec=7s
`,
	})
	errc := make(chan error, 1)
	go func() {
		_, err := m.Start(context.Background(), "late")
		errc <- err
	}()
	waitCond(t, func() bool { return len(launch.specs()) >= 1 })
	// TimeoutStartSec, not the engine's leftover maxWait. advanceWait
	// only checks fk.Waiting, so -race can Advance before waitReady arms.
	// Keep readiness distinct from the default 5s journal/stop deadline.
	waitCond(t, func() bool { return fk.WaitingAt(7 * time.Second) })
	fk.Advance(7 * time.Second)
	err := waitErr(t, errc)
	if err == nil {
		t.Fatal("expected TimeoutStartSec failure")
	}
	if !strings.Contains(err.Error(), "READY=1") && !strings.Contains(err.Error(), "TimeoutStartSec") {
		t.Fatalf("err = %v", err)
	}
	assertState(t, m, "late.service", core.Failed)
}

func TestStopUnblocksNotifyWaitReady(t *testing.T) {
	t.Parallel()
	launch := fakeNotifyLaunch()
	m, _ := managerWithFake(t, launch, map[string]string{
		"worker.service": `
[Service]
Type=notify
ExecStart=C:\App\worker.exe
WorkingDirectory=C:\App
TimeoutStartSec=5m
`,
	})
	errc := make(chan error, 1)
	go func() {
		_, err := m.Start(context.Background(), "worker")
		errc <- err
	}()
	waitNotifyPipe(t, launch, "worker.service")
	waitUntil(t, 2*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.stateOfLocked("worker.service") == core.Activating
	})
	done := make(chan error, 1)
	go func() {
		_, err := m.Stop("worker")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stop blocked behind waitReady holding the per-unit start lock")
	}
	if err := waitErr(t, errc); err == nil {
		t.Fatal("expected Start to fail after Stop")
	}
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
	if err := sendReadyUntilStart(t, pipe, errc); err != nil {
		t.Fatal(err)
	}

	waitCond(t, func() bool { return fk.WaitingAt(time.Second) })
	fk.Advance(400 * time.Millisecond)
	assertState(t, m, "hb.service", core.Active)
	sendWatchdogUntilArmed(t, pipe, fk)
	fk.Advance(time.Second)
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
	if err := sendReadyUntilStart(t, pipe, errc); err != nil {
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
	if err := sendReadyUntilStart(t, pipe, errc); err != nil {
		t.Fatal(err)
	}
	advanceWait(t, fk, time.Second)
	waitCond(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.stateOfLocked("miss.service") == core.Failed && m.subOfLocked("miss.service") == core.SubWatchdog
	})
}

func TestRedundantStartKeepsNotifyWatchdog(t *testing.T) {
	t.Parallel()
	launch := fakeNotifyLaunch()
	m, fk := managerWithFake(t, launch, map[string]string{
		"again.service": `
[Service]
Type=notify
ExecStart=C:\App\again.exe
WorkingDirectory=C:\App
TimeoutStartSec=5s
WatchdogSec=1s
Restart=no
`,
	})
	errc := make(chan error, 1)
	go func() {
		_, err := m.Start(context.Background(), "again")
		errc <- err
	}()
	pipe := waitNotifyPipe(t, launch, "again.service")
	if err := sendReadyUntilStart(t, pipe, errc); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "again.service", core.Active)
	gen := genOf(t, m, "again.service")
	if _, err := m.Start(context.Background(), "again"); err != nil {
		t.Fatal(err)
	}
	if got := genOf(t, m, "again.service"); got != gen {
		t.Fatalf("gen = %d after redundant Start, want %d", got, gen)
	}
	if n := len(launch.specs()); n != 1 {
		t.Fatalf("starts = %d, want 1", n)
	}
	advanceWait(t, fk, time.Second)
	waitCond(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.stateOfLocked("again.service") == core.Failed && m.subOfLocked("again.service") == core.SubWatchdog
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
	if err := sendReadyUntilStart(t, pipe, errc); err != nil {
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
[Unit]
StartLimitBurst=0
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
	waitState(t, m, "wd.service", core.Active)
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
	if err := sendReadyUntilStart(t, pipe, errc); err != nil {
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

// sendWatchdogUntilArmed sends WATCHDOG until the fake clock has a 1s
// wait (watchdogLoop Reset). One Send at a time so leftover pulses do
// not Reset again after the caller Advances (that left the unit Active).
func sendWatchdogUntilArmed(t *testing.T, pipe string, fk *timers.Fake) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if fk.WaitingAt(time.Second) {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = notify.SendRetry(ctx, pipe, notify.Message{Watchdog: true})
		cancel()
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("WATCHDOG pulse did not refresh timer")
}

// sendReadyUntilStart resends READY=1 until Start returns.
// This repeated sender supports lifecycle tests that race stop/relaunch.
// The transport acceptance handshake now protects individual short-lived
// sends; waitReady also retains readiness received before it starts waiting.
func sendReadyUntilStart(t *testing.T, pipe string, errc <-chan error) error {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = notify.SendRetry(ctx, pipe, notify.Message{Ready: true})
		cancel()
		select {
		case err := <-errc:
			return err
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatal("did not receive error result")
	return nil
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
	// once per distinct address. Resend across the lifecycle test's
	// stop/relaunch boundary so each invocation receives readiness.
	sent := 0
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && sent < 2 {
		specs := launch.specs()
		if sent >= len(specs) {
			time.Sleep(time.Millisecond)
			continue
		}
		spec := specs[sent]
		if spec.Unit != unit {
			time.Sleep(time.Millisecond)
			continue
		}
		pipe, ok := notify.LookupEnv(spec.Env, notify.EnvNotifyPipe)
		if !ok || pipe == "" {
			time.Sleep(time.Millisecond)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := notify.SendRetry(ctx, pipe, notify.Message{Ready: true})
		cancel()
		if err != nil {
			time.Sleep(time.Millisecond)
			continue
		}
		retryUntil := time.Now().Add(500 * time.Millisecond)
		for time.Now().Before(retryUntil) && time.Now().Before(deadline) {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			_ = notify.SendRetry(ctx, pipe, notify.Message{Ready: true})
			cancel()
			time.Sleep(20 * time.Millisecond)
		}
		sent++
	}
}
