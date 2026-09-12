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
	// Windows named-pipe Accept can return from Dial before waitReady is
	// selected; a single SendRetry then looks successful while Start is
	// still blocked (CI #42). Resend until Start returns.
	deadline := time.Now().Add(15 * time.Second)
	var startErr error
	got := false
	for !got && time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = notify.SendRetry(ctx, pipe, notify.Message{Ready: true})
		cancel()
		select {
		case startErr = <-errc:
			got = true
		case <-time.After(50 * time.Millisecond):
		}
	}
	if !got {
		t.Fatal("did not receive error result")
	}
	if startErr != nil {
		t.Fatal(startErr)
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
[Unit]
StartLimitBurst=0
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
	waitSub(t, m, "wd.service", core.SubAutoRestart)
	advanceArmed(t, fk, 2*time.Second)
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

func TestRedundantStartKeepsTCPWatchdog(t *testing.T) {
	ln, addr := listenLoopbackTCP(t)
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
	assertState(t, m, "tcp.service", core.Active)
	gen := genOf(t, m, "tcp.service")
	if _, err := m.Start(context.Background(), "tcp"); err != nil {
		t.Fatal(err)
	}
	if got := genOf(t, m, "tcp.service"); got != gen {
		t.Fatalf("gen = %d after redundant Start, want %d", got, gen)
	}
	if n := len(launch.specs()); n != 1 {
		t.Fatalf("starts = %d, want 1", n)
	}
	_ = ln.Close()
	advanceWait(t, fk, time.Second)
	waitWatchdogFailed(t, m, "tcp.service")
}

func TestTimerElapseDoesNotDisarmSimpleWatchdog(t *testing.T) {
	ln, addr := listenLoopbackTCP(t)
	launch := &fakeLauncher{}
	m, fk := managerWithFake(t, launch, map[string]string{
		"wd.service": fmt.Sprintf(`
[Service]
Type=simple
ExecStart=C:\App\wd.exe
WorkingDirectory=C:\App
WatchdogMode=tcp
WatchdogEndpoint=%s
WatchdogSec=2s
Restart=no
`, addr),
		"wd.timer": `
[Timer]
OnStartupSec=1s
`,
	})
	if _, err := m.Start(context.Background(), "wd.service"); err != nil {
		t.Fatal(err)
	}
	// probeWatchdogLoop arms WatchdogSec=2s on a goroutine. Wait until
	// that NewTimer is pending so later 1s Advances keep the original 2s
	// window (H0: do not Advance before the wait is armed).
	waitCond(t, func() bool { return fk.WaitingAt(2 * time.Second) })
	if _, err := m.Start(context.Background(), "wd.timer"); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "wd.service", core.Active)
	gen := genOf(t, m, "wd.service")
	// The watchdog's 2s wait does not prove the asynchronous timer-state
	// load and scheduler's 1s wait are ready. Advance only after that wait.
	waitCond(t, func() bool { return fk.WaitingAt(time.Second) })
	advanceArmed(t, fk, time.Second)
	// A scheduler wake can replace its relative wait concurrently with the
	// instantaneous fake advance. Deliver the clock observation explicitly.
	m.ClockChanged()
	waitCond(t, func() bool {
		st, err := m.Status("wd.timer")
		return err == nil && st.Unit != nil && st.Unit.Last != ""
	})
	if got := genOf(t, m, "wd.service"); got != gen {
		t.Fatalf("gen = %d after timer elapse Start, want %d", got, gen)
	}
	if n := len(launch.specs()); n != 1 {
		t.Fatalf("starts = %d after timer elapse, want 1", n)
	}
	assertState(t, m, "wd.service", core.Active)
	_ = ln.Close()
	advanceArmed(t, fk, time.Second)
	waitWatchdogFailed(t, m, "wd.service")
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
		err := m.errOfLocked(name)
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
	err := m.errOfLocked(name)
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
