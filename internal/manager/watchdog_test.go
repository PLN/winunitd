package manager

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/notify"
	"github.com/PLN/winunitd/internal/unit"
)

func TestTCPWatchdogConnectKeepsActive(t *testing.T) {
	_, addr := listenLoopbackTCP(t)
	launch := &fakeLauncher{}
	m, fk := managerWithFake(t, launch, map[string]string{
		"tcp.service": fmt.Sprintf(`
[Service]
Type=simple
ExecStart=C:\App\tcp.exe
WorkingDirectory=C:\App
WatchdogMode=tcp
WatchdogEndpoint=%s
WatchdogSec=1s
Restart=no
`, addr),
	})
	if _, err := m.Start(context.Background(), "tcp"); err != nil {
		t.Fatal(err)
	}
	advanceWait(t, fk, time.Second)
	assertWatchdogActive(t, m, "tcp.service")
	if _, ok := notify.LookupEnv(launch.specs()[0].Env, notify.EnvNotifyPipe); ok {
		t.Fatal("tcp watchdog must not inject WINUNIT_NOTIFY_PIPE")
	}
}

func TestTCPWatchdogRefusedFails(t *testing.T) {
	addr := closedLoopbackTCP(t)
	launch := &fakeLauncher{}
	m, fk := managerWithFake(t, launch, map[string]string{
		"refused.service": fmt.Sprintf(`
[Service]
Type=simple
ExecStart=C:\App\refused.exe
WorkingDirectory=C:\App
WatchdogMode=tcp
WatchdogEndpoint=%s
WatchdogSec=1s
Restart=no
`, addr),
	})
	if _, err := m.Start(context.Background(), "refused"); err != nil {
		t.Fatal(err)
	}
	advanceWait(t, fk, time.Second)
	waitWatchdogFailed(t, m, "refused.service")
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
WatchdogEndpoint=192.0.2.1:80
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
	ep := serveWatchdogHTTP(t, 200)
	launch := &fakeLauncher{}
	m, fk := managerWithFake(t, launch, map[string]string{
		"http.service": fmt.Sprintf(`
[Service]
Type=simple
ExecStart=C:\App\http.exe
WorkingDirectory=C:\App
WatchdogMode=http
WatchdogEndpoint=%s
WatchdogSec=1s
Restart=no
`, ep),
	})
	if _, err := m.Start(context.Background(), "http"); err != nil {
		t.Fatal(err)
	}
	advanceWait(t, fk, time.Second)
	assertWatchdogActive(t, m, "http.service")
}

func TestHTTPWatchdogWrongStatusFails(t *testing.T) {
	ep := serveWatchdogHTTP(t, 503)
	launch := &fakeLauncher{}
	m, fk := managerWithFake(t, launch, map[string]string{
		"httpbad.service": fmt.Sprintf(`
[Service]
Type=simple
ExecStart=C:\App\http.exe
WorkingDirectory=C:\App
WatchdogMode=http
WatchdogEndpoint=%s
WatchdogExpectedStatus=200
WatchdogSec=1s
Restart=no
`, ep),
	})
	if _, err := m.Start(context.Background(), "httpbad"); err != nil {
		t.Fatal(err)
	}
	advanceWait(t, fk, time.Second)
	waitWatchdogFailed(t, m, "httpbad.service")
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
	_, addr := listenLoopbackTCP(t)
	launch := fakeNotifyLaunch()
	m, fk := managerWithFake(t, launch, map[string]string{
		"both.service": fmt.Sprintf(`
[Service]
Type=notify
ExecStart=C:\App\both.exe
WorkingDirectory=C:\App
TimeoutStartSec=5s
WatchdogMode=tcp
WatchdogEndpoint=%s
WatchdogSec=1s
Restart=no
`, addr),
	})
	errc := make(chan error, 1)
	go func() {
		_, err := m.Start(context.Background(), "both")
		errc <- err
	}()
	pipe := waitNotifyPipe(t, launch, "both.service")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := notify.SendRetry(ctx, pipe, notify.Message{Ready: true}); err != nil {
		t.Fatal(err)
	}
	if err := waitErr(t, errc); err != nil {
		t.Fatal(err)
	}
	if _, ok := notify.LookupEnv(launch.specs()[0].Env, notify.EnvWatchdogUsec); ok {
		t.Fatal("tcp watchdog must not inject WINUNIT_WATCHDOG_USEC")
	}
	advanceWait(t, fk, time.Second)
	assertWatchdogActive(t, m, "both.service")
}

func TestTCPWatchdogRestartOnWatchdog(t *testing.T) {
	addr := closedLoopbackTCP(t)
	launch := &fakeLauncher{}
	m, fk := managerWithFake(t, launch, map[string]string{
		"wd.service": fmt.Sprintf(`
[Service]
Type=simple
ExecStart=C:\App\wd.exe
WorkingDirectory=C:\App
WatchdogMode=tcp
WatchdogEndpoint=%s
WatchdogSec=1s
Restart=on-watchdog
RestartSec=2s
`, addr),
	})
	if _, err := m.Start(context.Background(), "wd"); err != nil {
		t.Fatal(err)
	}
	advanceWait(t, fk, time.Second)
	waitCond(t, func() bool { return len(launch.specs()) >= 1 })
	advanceWait(t, fk, 2*time.Second)
	waitCond(t, func() bool { return len(launch.specs()) >= 2 })
}

func TestTCPWatchdogIPv6Loopback(t *testing.T) {
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skip("IPv6 loopback not available")
	}
	acceptLoopback(t, ln)
	launch := &fakeLauncher{}
	m, fk := managerWithFake(t, launch, map[string]string{
		"v6.service": fmt.Sprintf(`
[Service]
Type=simple
ExecStart=C:\App\v6.exe
WorkingDirectory=C:\App
WatchdogMode=tcp
WatchdogEndpoint=%s
WatchdogSec=1s
Restart=no
`, ln.Addr().String()),
	})
	if _, err := m.Start(context.Background(), "v6"); err != nil {
		t.Fatal(err)
	}
	advanceWait(t, fk, time.Second)
	assertWatchdogActive(t, m, "v6.service")
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
WatchdogEndpoint=192.0.2.1:443
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

func listenLoopbackTCP(t *testing.T) (net.Listener, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	acceptLoopback(t, ln)
	addr := ln.Addr().String()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := unit.ProbeTCP(ctx, addr); err != nil {
		t.Fatalf("listen probe: %v", err)
	}
	return ln, addr
}

func acceptLoopback(t *testing.T, ln net.Listener) {
	t.Helper()
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
}

func closedLoopbackTCP(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := unit.ProbeTCP(ctx, addr); err == nil {
		t.Skip("port was reused after close; cannot assert refused")
	}
	return addr
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
	ep := "http://" + ln.Addr().String() + "/health"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	waitCond(t, func() bool {
		return unit.ProbeHTTP(ctx, ep, status) == nil
	})
	return ep
}

func assertWatchdogActive(t *testing.T, m *Manager, name string) {
	t.Helper()
	for i := 0; i < 100_000; i++ {
		m.mu.Lock()
		st := m.stateOfLocked(name)
		sub := m.subOfLocked(name)
		err := m.errors[name]
		m.mu.Unlock()
		if st == core.Failed {
			t.Fatalf("state = %s sub=%s error=%q, want active", st, sub, err)
		}
		runtime.Gosched()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.stateOfLocked(name)
	sub := m.subOfLocked(name)
	err := m.errors[name]
	if st != core.Active {
		t.Fatalf("state = %s sub=%s error=%q, want active", st, sub, err)
	}
}

func waitWatchdogFailed(t *testing.T, m *Manager, name string) {
	t.Helper()
	waitCond(t, func() bool {
		m.mu.Lock()
		ok := m.stateOfLocked(name) == core.Failed && m.subOfLocked(name) == core.SubWatchdog
		m.mu.Unlock()
		return ok
	})
}
