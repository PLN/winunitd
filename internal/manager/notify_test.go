package manager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/notify"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/unit"
)

func TestNotifyStaysActivatingUntilReady(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	m := notifyManagerWith(t, launch, map[string]string{
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

	pipe := waitNotifyPipe(t, launch, "worker.service", 2*time.Second)
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
	launch := &fakeLauncher{}
	m := notifyManagerWith(t, launch, map[string]string{
		"late.service": `
[Service]
Type=notify
ExecStart=C:\App\late.exe
WorkingDirectory=C:\App
TimeoutStartSec=200ms
`,
	})
	_, err := m.Start(context.Background(), "late")
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
	launch := &fakeLauncher{}
	m := notifyManagerWith(t, launch, map[string]string{
		"hb.service": `
[Service]
Type=notify
ExecStart=C:\App\hb.exe
WorkingDirectory=C:\App
TimeoutStartSec=5s
WatchdogSec=250ms
`,
	})
	errc := make(chan error, 1)
	go func() {
		_, err := m.Start(context.Background(), "hb")
		errc <- err
	}()
	pipe := waitNotifyPipe(t, launch, "hb.service", 2*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := notify.SendRetry(ctx, pipe, notify.Message{Ready: true}); err != nil {
		t.Fatal(err)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}

	pulseDone := make(chan struct{})
	go func() {
		defer close(pulseDone)
		deadline := time.Now().Add(600 * time.Millisecond)
		for time.Now().Before(deadline) {
			_ = notify.Send(ctx, pipe, notify.Message{Watchdog: true})
			time.Sleep(50 * time.Millisecond)
		}
	}()
	<-pulseDone
	assertState(t, m, "hb.service", core.Active)

	waitUntil(t, 2*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.stateOfLocked("hb.service") == core.Failed
	})
}

func TestMissedWatchdogSecFailsUnit(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	m := notifyManagerWith(t, launch, map[string]string{
		"miss.service": `
[Service]
Type=notify
ExecStart=C:\App\miss.exe
WorkingDirectory=C:\App
TimeoutStartSec=5s
WatchdogSec=150ms
Restart=no
`,
	})
	errc := make(chan error, 1)
	go func() {
		_, err := m.Start(context.Background(), "miss")
		errc <- err
	}()
	pipe := waitNotifyPipe(t, launch, "miss.service", 2*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := notify.SendRetry(ctx, pipe, notify.Message{Ready: true}); err != nil {
		t.Fatal(err)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 2*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.stateOfLocked("miss.service") == core.Failed && m.subOfLocked("miss.service") == core.SubWatchdog
	})
}

func TestRestartOnWatchdogRelaunches(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	m := notifyManagerWith(t, launch, map[string]string{
		"wd.service": `
[Service]
Type=notify
ExecStart=C:\App\wd.exe
WorkingDirectory=C:\App
TimeoutStartSec=5s
WatchdogSec=120ms
Restart=on-watchdog
RestartSec=20ms
`,
	})
	go keepReady(t, launch, "wd.service")
	if _, err := m.Start(context.Background(), "wd"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 3*time.Second, func() bool { return len(launch.specs()) >= 2 })
}

func TestNotifyInjectsEnv(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	m := notifyManagerWith(t, launch, map[string]string{
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
	pipe := waitNotifyPipe(t, launch, "env.service", 2*time.Second)
	if pipe == "" {
		t.Fatal("WINUNIT_NOTIFY_PIPE missing")
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
	launch := &fakeLauncher{}
	m := notifyManagerWith(t, launch, map[string]string{
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
	pipe := waitNotifyPipe(t, launch, "cli.service", 2*time.Second)

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

// notifyManagerWith uses a fake TCP notify listener so portable tests can
// send READY/WATCHDOG from the test process. On Windows, NotifyAccess=main
// would otherwise reject that PID (named-pipe ClientPID). Windows
// CreateProcess coverage lives in manager_windows_test.go.
func notifyManagerWith(t *testing.T, launch runtime.Launcher, files map[string]string) *Manager {
	t.Helper()
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		writeUnit(t, units, name, body)
	}
	m, err := New(Config{
		BaseDir: dir,
		Launch:  launch,
		NotifyListen: func(string) (notify.Listener, error) {
			return notify.ListenTCP()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	return m
}

func waitNotifyPipe(t *testing.T, launch *fakeLauncher, unit string, timeout time.Duration) string {
	t.Helper()
	var pipe string
	waitUntil(t, timeout, func() bool {
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
	seen := map[string]bool{}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, spec := range launch.specs() {
			if spec.Unit != unit {
				continue
			}
			pipe, ok := notify.LookupEnv(spec.Env, notify.EnvNotifyPipe)
			if !ok || pipe == "" || seen[pipe] {
				continue
			}
			seen[pipe] = true
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = notify.SendRetry(ctx, pipe, notify.Message{Ready: true})
			cancel()
		}
		if len(seen) >= 2 {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
}
