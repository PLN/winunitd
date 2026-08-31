package manager

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/notify"
	"github.com/PLN/winunitd/internal/unit"
)

func TestTCPWatchdogConnectKeepsActive(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	launch := &fakeLauncher{}
	m := managerWith(t, launch, map[string]string{
		"tcp.service": fmt.Sprintf(`
[Service]
Type=simple
ExecStart=C:\App\tcp.exe
WorkingDirectory=C:\App
WatchdogMode=tcp
WatchdogEndpoint=%s
WatchdogSec=120ms
Restart=no
`, ln.Addr().String()),
	})
	if _, err := m.Start(context.Background(), "tcp"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	assertState(t, m, "tcp.service", core.Active)
	if _, ok := notify.LookupEnv(launch.specs()[0].Env, notify.EnvNotifyPipe); ok {
		t.Fatal("tcp watchdog must not inject WINUNIT_NOTIFY_PIPE")
	}
}

func TestTCPWatchdogRefusedFails(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	launch := &fakeLauncher{}
	m := managerWith(t, launch, map[string]string{
		"refused.service": fmt.Sprintf(`
[Service]
Type=simple
ExecStart=C:\App\refused.exe
WorkingDirectory=C:\App
WatchdogMode=tcp
WatchdogEndpoint=%s
WatchdogSec=80ms
Restart=no
`, addr),
	})
	if _, err := m.Start(context.Background(), "refused"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 2*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.stateOfLocked("refused.service") == core.Failed && m.subOfLocked("refused.service") == core.SubWatchdog
	})
}

func TestTCPWatchdogWrongHostNotLoaded(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	m := managerWith(t, launch, map[string]string{
		"bad.service": `
[Service]
Type=simple
ExecStart=C:\App\bad.exe
WorkingDirectory=C:\App
WatchdogMode=tcp
WatchdogEndpoint=8.8.8.8:80
WatchdogSec=1s
`,
	})
	_, err := m.Start(context.Background(), "bad")
	if err == nil {
		t.Fatal("non-loopback WatchdogEndpoint must not load")
	}
	if len(launch.specs()) != 0 {
		t.Fatal("wrong-host must not start or probe the network")
	}
}

func TestHTTPWatchdog200KeepsActive(t *testing.T) {
	t.Parallel()
	ep := serveWatchdogHTTP(t, 200)
	launch := &fakeLauncher{}
	m := managerWith(t, launch, map[string]string{
		"http.service": fmt.Sprintf(`
[Service]
Type=simple
ExecStart=C:\App\http.exe
WorkingDirectory=C:\App
WatchdogMode=http
WatchdogEndpoint=%s
WatchdogSec=120ms
Restart=no
`, ep),
	})
	if _, err := m.Start(context.Background(), "http"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	assertState(t, m, "http.service", core.Active)
}

func TestHTTPWatchdogWrongStatusFails(t *testing.T) {
	t.Parallel()
	ep := serveWatchdogHTTP(t, 503)
	launch := &fakeLauncher{}
	m := managerWith(t, launch, map[string]string{
		"httpbad.service": fmt.Sprintf(`
[Service]
Type=simple
ExecStart=C:\App\http.exe
WorkingDirectory=C:\App
WatchdogMode=http
WatchdogEndpoint=%s
WatchdogExpectedStatus=200
WatchdogSec=80ms
Restart=no
`, ep),
	})
	if _, err := m.Start(context.Background(), "httpbad"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 2*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.stateOfLocked("httpbad.service") == core.Failed && m.subOfLocked("httpbad.service") == core.SubWatchdog
	})
}

func TestHTTPWatchdogWrongHostNotLoaded(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	m := managerWith(t, launch, map[string]string{
		"remote.service": `
[Service]
Type=simple
ExecStart=C:\App\remote.exe
WorkingDirectory=C:\App
WatchdogMode=http
WatchdogEndpoint=http://example.com/health
WatchdogSec=1s
`,
	})
	_, err := m.Start(context.Background(), "remote")
	if err == nil {
		t.Fatal("non-loopback WatchdogEndpoint must not load")
	}
	if len(launch.specs()) != 0 {
		t.Fatal("wrong-host must not start or probe the network")
	}
}

func TestNotifyReadyWithTCPWatchdog(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	launch := fakeNotifyLaunch()
	m := managerWith(t, launch, map[string]string{
		"both.service": fmt.Sprintf(`
[Service]
Type=notify
ExecStart=C:\App\both.exe
WorkingDirectory=C:\App
TimeoutStartSec=5s
WatchdogMode=tcp
WatchdogEndpoint=%s
WatchdogSec=120ms
Restart=no
`, ln.Addr().String()),
	})
	errc := make(chan error, 1)
	go func() {
		_, err := m.Start(context.Background(), "both")
		errc <- err
	}()
	pipe := waitNotifyPipe(t, launch, "both.service", 2*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := notify.SendRetry(ctx, pipe, notify.Message{Ready: true}); err != nil {
		t.Fatal(err)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if _, ok := notify.LookupEnv(launch.specs()[0].Env, notify.EnvWatchdogUsec); ok {
		t.Fatal("tcp watchdog must not inject WINUNIT_WATCHDOG_USEC")
	}
	time.Sleep(400 * time.Millisecond)
	assertState(t, m, "both.service", core.Active)
}

func TestTCPWatchdogRestartOnWatchdog(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	launch := &fakeLauncher{}
	m := managerWith(t, launch, map[string]string{
		"wd.service": fmt.Sprintf(`
[Service]
Type=simple
ExecStart=C:\App\wd.exe
WorkingDirectory=C:\App
WatchdogMode=tcp
WatchdogEndpoint=%s
WatchdogSec=80ms
Restart=on-watchdog
RestartSec=20ms
`, addr),
	})
	if _, err := m.Start(context.Background(), "wd"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 3*time.Second, func() bool { return len(launch.specs()) >= 2 })
}

func TestTCPWatchdogIPv6Loopback(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skip("IPv6 loopback not available")
	}
	t.Cleanup(func() { _ = ln.Close() })
	launch := &fakeLauncher{}
	m := managerWith(t, launch, map[string]string{
		"v6.service": fmt.Sprintf(`
[Service]
Type=simple
ExecStart=C:\App\v6.exe
WorkingDirectory=C:\App
WatchdogMode=tcp
WatchdogEndpoint=%s
WatchdogSec=120ms
Restart=no
`, ln.Addr().String()),
	})
	if _, err := m.Start(context.Background(), "v6"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	assertState(t, m, "v6.service", core.Active)
}

func serveWatchdogHTTP(t *testing.T, status int) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, "p5")
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return "http://" + ln.Addr().String() + "/health"
}

func TestWatchdogProbeModeHelper(t *testing.T) {
	t.Parallel()
	s := &unit.ServiceSpec{WatchdogMode: unit.WatchdogModeTCP}
	if !s.WatchdogProbeMode() {
		t.Fatal("tcp")
	}
	s.WatchdogMode = unit.WatchdogModeNotify
	if s.WatchdogProbeMode() {
		t.Fatal("notify is not a probe mode")
	}
}

func TestTCPWatchdogEndpointMustBeLoopbackInError(t *testing.T) {
	t.Parallel()
	rep := unit.ParseUnit("x.service", `
[Service]
ExecStart=C:\App\x.exe
WorkingDirectory=C:\App
WatchdogMode=tcp
WatchdogEndpoint=1.1.1.1:443
WatchdogSec=1s
`)
	if !rep.HasError() {
		t.Fatal("expected parse error")
	}
	var b strings.Builder
	for _, iss := range rep.Errors() {
		b.WriteString(iss.Message)
	}
	if !strings.Contains(b.String(), "loopback") {
		t.Fatalf("errors = %s", b.String())
	}
}
