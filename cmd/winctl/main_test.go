package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/manager"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
)

func TestRunHelp(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"--help"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), "verify") {
		t.Fatalf("help missing verify: %s", out.String())
	}
	if !strings.Contains(out.String(), "list-units") {
		t.Fatalf("help missing list-units: %s", out.String())
	}
	if !strings.Contains(out.String(), "--user") {
		t.Fatalf("help missing --user: %s", out.String())
	}
	if !strings.Contains(out.String(), "enable-linger") {
		t.Fatalf("help missing enable-linger: %s", out.String())
	}
	if !strings.Contains(out.String(), `\\.\pipe\winunitd\user\`) {
		t.Fatalf("help missing user pipe: %s", out.String())
	}
}

func TestRunVerify(t *testing.T) {
	dir := t.TempDir()
	ok := filepath.Join(dir, "ok.service")
	if err := os.WriteFile(ok, []byte(`
[Service]
Type=simple
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`), 0o644); err != nil {
		t.Fatal(err)
	}
	warn := filepath.Join(dir, "warn.service")
	if err := os.WriteFile(warn, []byte(`
[Service]
ExecStart=C:\Tools\foo.exe
`), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "bad.service")
	if err := os.WriteFile(bad, []byte(`
[Service]
ExecStart=foo.exe
WatchdogSec=1s
`), 0o644); err != nil {
		t.Fatal(err)
	}
	timer := filepath.Join(dir, "foo.timer")
	if err := os.WriteFile(timer, []byte(`
[Timer]
OnCalendar=daily
`), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("ok", func(t *testing.T) {
		var out, errb bytes.Buffer
		code := run([]string{"verify", ok}, &out, &errb)
		if code != 0 {
			t.Fatalf("exit %d stdout=%s stderr=%s", code, out.String(), errb.String())
		}
		if !strings.Contains(out.String(), "ok.service: verified") {
			t.Fatalf("stdout=%s", out.String())
		}
	})

	t.Run("working directory warning still verifies", func(t *testing.T) {
		var out, errb bytes.Buffer
		code := run([]string{"verify", warn}, &out, &errb)
		if code != 0 {
			t.Fatalf("exit %d stdout=%s stderr=%s", code, out.String(), errb.String())
		}
		if !strings.Contains(errb.String(), "WorkingDirectory is omitted") {
			t.Fatalf("stderr=%s", errb.String())
		}
		if !strings.Contains(out.String(), "warn.service: verified") {
			t.Fatalf("stdout=%s", out.String())
		}
	})

	t.Run("unknown and relative fail", func(t *testing.T) {
		var out, errb bytes.Buffer
		code := run([]string{"verify", bad}, &out, &errb)
		if code != 1 {
			t.Fatalf("exit %d, want 1; stdout=%s stderr=%s", code, out.String(), errb.String())
		}
		s := errb.String()
		if !strings.Contains(s, "absolute path") {
			t.Fatalf("missing relative ExecStart: %s", s)
		}
		if !strings.Contains(s, `unknown directive "WatchdogSec"`) {
			t.Fatalf("unknown directive should fail: %s", s)
		}
		if strings.Contains(out.String(), ": verified") {
			t.Fatalf("failed unit should not be verified: %s", out.String())
		}
	})

	t.Run("timer implicit unit", func(t *testing.T) {
		var out, errb bytes.Buffer
		code := run([]string{"verify", timer}, &out, &errb)
		if code != 0 {
			t.Fatalf("exit %d stdout=%s", code, out.String())
		}
		if !strings.Contains(out.String(), "foo.timer: verified") {
			t.Fatalf("stdout=%s", out.String())
		}
	})

	t.Run("missing path", func(t *testing.T) {
		var out, errb bytes.Buffer
		code := run([]string{"verify"}, &out, &errb)
		if code != 2 {
			t.Fatalf("exit %d", code)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		var out, errb bytes.Buffer
		code := run([]string{"verify", filepath.Join(dir, "absent.service")}, &out, &errb)
		if code != 1 {
			t.Fatalf("exit %d stdout=%s stderr=%s", code, out.String(), errb.String())
		}
		if !strings.Contains(errb.String(), "absent.service") {
			t.Fatalf("stderr=%s", errb.String())
		}
	})
}

func TestStartWithoutDaemon(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"start", "foo"}, &out, &errb)
	if code == 0 {
		t.Fatalf("start without daemon should fail; stderr=%s", errb.String())
	}
	if !strings.Contains(errb.String(), "cannot connect to winunitd") {
		t.Fatalf("stderr=%s", errb.String())
	}
}

func TestUnknownCommand(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"not-a-verb"}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit %d", code)
	}
}

func TestCLIListUnitsOverPipe(t *testing.T) {
	m, dial, stop := startTestDaemon(t)
	defer stop()
	_ = m

	var out, errb bytes.Buffer
	code := runCLI([]string{"list-units"}, &out, &errb, dial)
	if code != 0 {
		t.Fatalf("exit %d stdout=%s stderr=%s", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "foo.service") {
		t.Fatalf("stdout=%s", out.String())
	}
	if !strings.Contains(out.String(), "inactive") {
		t.Fatalf("stdout=%s", out.String())
	}
}

func TestCLIUserFlagDialsUserManager(t *testing.T) {
	_, sysDial, sysStop := startTestDaemonUnit(t, `
[Unit]
Description=SystemFoo
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`, runtime.StubLauncher())
	defer sysStop()

	userDir := t.TempDir()
	units := filepath.Join(userDir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(units, "hermes.service"), []byte(`
[Unit]
Description=Hermes
[Service]
Type=oneshot
ExecStart=C:\Tools\hermes.exe
WorkingDirectory=C:\Tools
[Install]
WantedBy=default.target
`), 0o644); err != nil {
		t.Fatal(err)
	}
	um, err := manager.New(manager.Config{
		BaseDir:               userDir,
		Launch:                runtime.StubLauncher(),
		UserScope:             true,
		HasInteractiveSession: func() bool { return false },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := um.Reload(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() { errc <- protocol.Serve(ctx, lis, um, protocol.AllowOwner) }()
	userDial := func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, lis.Addr().Network(), lis.Addr().String())
	}
	defer func() {
		um.Close()
		cancel()
		_ = lis.Close()
		select {
		case <-errc:
		case <-time.After(2 * time.Second):
		}
	}()

	var out, errb bytes.Buffer
	code := runCLIUser([]string{"--user", "list-units"}, &out, &errb, sysDial, userDial)
	if code != 0 {
		t.Fatalf("user list exit %d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "hermes.service") {
		t.Fatalf("user list missing hermes: %s", out.String())
	}
	if !strings.Contains(out.String(), "graphical-session.target") {
		t.Fatalf("user list missing graphical-session.target: %s", out.String())
	}
	if strings.Contains(out.String(), "foo.service") {
		t.Fatalf("user list must not show system units: %s", out.String())
	}

	out.Reset()
	errb.Reset()
	code = runCLIUser([]string{"--user", "status", "graphical-session.target"}, &out, &errb, sysDial, userDial)
	if code != 0 {
		t.Fatalf("user status exit %d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "graphical-session.target") {
		t.Fatalf("user status missing target: %s", out.String())
	}
	if !strings.Contains(out.String(), "inactive") {
		t.Fatalf("linger-without-session status = %s", out.String())
	}

	out.Reset()
	errb.Reset()
	code = runCLIUser([]string{"list-units"}, &out, &errb, sysDial, userDial)
	if code != 0 {
		t.Fatalf("system list exit %d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "foo.service") {
		t.Fatalf("bare winctl should hit the system pipe: %s", out.String())
	}
	if strings.Contains(out.String(), "hermes.service") {
		t.Fatalf("system list-units must not show user units: %s", out.String())
	}

	out.Reset()
	errb.Reset()
	code = runCLIUser([]string{"--user", "enable", "hermes"}, &out, &errb, sysDial, userDial)
	if code != 0 {
		t.Fatalf("enable exit %d stderr=%s", code, errb.String())
	}
	out.Reset()
	errb.Reset()
	code = runCLIUser([]string{"--user", "start", "hermes"}, &out, &errb, sysDial, userDial)
	if code != 0 {
		t.Fatalf("start exit %d stderr=%s", code, errb.String())
	}
}

func TestCLILogsOverPipe(t *testing.T) {
	_, dial, stop := startTestDaemonUnit(t, `
[Unit]
Description=Foo
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`, runtime.StubLauncherOutput("hello from unit\n", "warn from unit\n"))
	defer stop()

	var out, errb bytes.Buffer
	code := runCLI([]string{"start", "foo"}, &out, &errb, dial)
	if code != 0 {
		t.Fatalf("start exit %d stderr=%s", code, errb.String())
	}

	deadline := time.Now().Add(2 * time.Second)
	var logs string
	for time.Now().Before(deadline) {
		out.Reset()
		errb.Reset()
		code = runCLI([]string{"logs", "foo"}, &out, &errb, dial)
		if code != 0 {
			t.Fatalf("logs exit %d stderr=%s", code, errb.String())
		}
		logs = out.String()
		if strings.Contains(logs, "hello from unit") && strings.Contains(logs, "warn from unit") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(logs, "hello from unit") {
		t.Fatalf("logs missing stdout: %s", logs)
	}
	if !strings.Contains(logs, "warn from unit") {
		t.Fatalf("logs missing stderr: %s", logs)
	}

	out.Reset()
	errb.Reset()
	code = runCLI([]string{"list-units"}, &out, &errb, dial)
	if code != 0 {
		t.Fatalf("list exit %d stderr=%s", code, errb.String())
	}
	list := out.String()
	if !strings.Contains(list, "foo.service") || !strings.Contains(list, "active") {
		t.Fatalf("list=%s", list)
	}
	if !strings.Contains(list, "1") {
		t.Fatalf("list missing pid: %s", list)
	}
}

func TestCLIStartAndStatusOverPipe(t *testing.T) {
	// Start is real CreateProcess. Use ping.exe so this is valid on
	// windows-latest; Linux DefaultLauncher is a stub and ignores the path.
	_, dial, stop := startTestDaemonUnit(t, `
[Unit]
Description=Foo
[Service]
Type=simple
ExecStart=C:\Windows\System32\ping.exe
ExecStartArg=-t
ExecStartArg=127.0.0.1
WorkingDirectory=C:\Windows\System32
`, nil)
	defer stop()

	var out, errb bytes.Buffer
	code := runCLI([]string{"start", "foo"}, &out, &errb, dial)
	if code != 0 {
		t.Fatalf("start exit %d stderr=%s", code, errb.String())
	}

	out.Reset()
	errb.Reset()
	code = runCLI([]string{"status", "foo"}, &out, &errb, dial)
	if code != 0 {
		t.Fatalf("status exit %d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "foo.service") || !strings.Contains(out.String(), "active") {
		t.Fatalf("stdout=%s", out.String())
	}
	if !strings.Contains(out.String(), "Main PID:") {
		t.Fatalf("status missing Main PID: %s", out.String())
	}
}

func TestCLIVerifyUnitNameOverPipe(t *testing.T) {
	_, dial, stop := startTestDaemon(t)
	defer stop()

	var out, errb bytes.Buffer
	code := runCLI([]string{"verify", "foo.service"}, &out, &errb, dial)
	if code != 0 {
		t.Fatalf("exit %d stdout=%s stderr=%s", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "foo.service: verified") {
		t.Fatalf("stdout=%s", out.String())
	}
}

func TestCLIDoesNotParseCLIAsAPI(t *testing.T) {
	// The CLI is a client of the versioned RPC. A raw protocol call must
	// succeed without involving winctl output.
	_, dial, stop := startTestDaemon(t)
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := protocol.NewClient(conn)
	got, err := client.ListUnits(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Units) == 0 {
		t.Fatal("expected units from RPC")
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var decoded protocol.ListUnitsResult
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Units[0].Name != got.Units[0].Name {
		t.Fatalf("round trip = %+v", decoded)
	}
}

func startTestDaemon(t *testing.T) (*manager.Manager, func(context.Context) (net.Conn, error), func()) {
	t.Helper()
	return startTestDaemonUnit(t, `
[Unit]
Description=Foo
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`, runtime.StubLauncher())
}

func startTestDaemonUnit(t *testing.T, unitBody string, launch runtime.Launcher) (*manager.Manager, func(context.Context) (net.Conn, error), func()) {
	t.Helper()
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(units, "foo.service"), []byte(unitBody), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := manager.New(manager.Config{BaseDir: dir, Launch: launch})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() { errc <- protocol.Serve(ctx, lis, m, protocol.AllowAdmin) }()
	dial := func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, lis.Addr().Network(), lis.Addr().String())
	}
	stop := func() {
		_, _ = m.Stop("foo")
		m.Close()
		cancel()
		_ = lis.Close()
		select {
		case <-errc:
		case <-time.After(2 * time.Second):
		}
	}
	return m, dial, stop
}

func TestCLIEnableLingerUsesSystemPipe(t *testing.T) {
	m, _, stop := startTestDaemon(t)
	defer stop()

	dir := t.TempDir()
	h := manager.NewUserHost(manager.UserHostConfig{
		Exe:       "winunitd-test",
		LingerDir: dir,
		QueryToken: func(sessionID uint32) (*runtime.UserToken, error) {
			return nil, runtime.ErrNoUserToken
		},
		Start: func(spec runtime.UserManagerSpec) (runtime.UserManagerProc, error) {
			p := &cliFakeMgr{sid: spec.SID}
			p.alive.Store(true)
			return p, nil
		},
		Sessions: func() ([]uint32, error) { return nil, nil },
		Lookup: func(name string) (runtime.UserInfo, error) {
			return runtime.UserInfo{SID: "S-1-5-21-1-2-3-1001", Username: "alice", Domain: "TEST", Profile: `C:\Users\alice`}, nil
		},
		LingerToken: func(rec runtime.LingerRecord) (*runtime.UserToken, error) {
			return &runtime.UserToken{Info: runtime.UserInfo{SID: rec.SID, Username: "alice", Domain: "TEST", Profile: `C:\Users\alice`}}, nil
		},
	})
	t.Cleanup(h.Close)

	ctrl := &manager.Control{Units: m, Users: h}
	ctx, cancel := context.WithCancel(context.Background())
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() { errc <- protocol.Serve(ctx, lis, ctrl, protocol.AllowAdmin) }()
	lingerDial := func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, lis.Addr().Network(), lis.Addr().String())
	}
	defer func() {
		cancel()
		_ = lis.Close()
		select {
		case <-errc:
		case <-time.After(2 * time.Second):
		}
	}()

	var out, errb bytes.Buffer
	code := runCLI([]string{"enable-linger", "alice"}, &out, &errb, lingerDial)
	if code != 0 {
		t.Fatalf("exit %d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "lingering") {
		t.Fatalf("stdout=%s", out.String())
	}
	if !h.Alive("S-1-5-21-1-2-3-1001") {
		t.Fatal("manager should start at enable-linger")
	}

	out.Reset()
	errb.Reset()
	code = runCLIUser([]string{"--user", "enable-linger", "alice"}, &out, &errb, lingerDial, lingerDial)
	if code != 2 {
		t.Fatalf("--user enable-linger exit %d want 2; stderr=%s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "system pipe") {
		t.Fatalf("stderr=%s", errb.String())
	}

	out.Reset()
	errb.Reset()
	code = runCLI([]string{"disable-linger", "alice"}, &out, &errb, lingerDial)
	if code != 0 {
		t.Fatalf("disable exit %d stderr=%s", code, errb.String())
	}
}

func TestCLIEnableLingerNonAdminDenied(t *testing.T) {
	m, _, stop := startTestDaemon(t)
	defer stop()
	dir := t.TempDir()
	h := manager.NewUserHost(manager.UserHostConfig{
		Exe:       "winunitd-test",
		LingerDir: dir,
		Start: func(spec runtime.UserManagerSpec) (runtime.UserManagerProc, error) {
			t.Fatal("must not start")
			return nil, nil
		},
		Lookup: func(name string) (runtime.UserInfo, error) {
			return runtime.UserInfo{SID: "S-1-5-21-1-2-3-1001", Username: "alice", Domain: "TEST"}, nil
		},
	})
	t.Cleanup(h.Close)
	ctrl := &manager.Control{Units: m, Users: h}
	ctx, cancel := context.WithCancel(context.Background())
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() { errc <- protocol.Serve(ctx, lis, ctrl, protocol.AllowOwner) }()
	dial := func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, lis.Addr().Network(), lis.Addr().String())
	}
	defer func() {
		cancel()
		_ = lis.Close()
		select {
		case <-errc:
		case <-time.After(2 * time.Second):
		}
	}()

	var out, errb bytes.Buffer
	code := runCLI([]string{"enable-linger", "alice"}, &out, &errb, dial)
	if code == 0 {
		t.Fatal("non-admin enable-linger must fail closed")
	}
	if !strings.Contains(errb.String(), "access denied") && !strings.Contains(errb.String(), "permission") {
		t.Fatalf("stderr=%s", errb.String())
	}
}

type cliFakeMgr struct {
	sid   string
	alive atomic.Bool
}

func (p *cliFakeMgr) PID() int    { return 1 }
func (p *cliFakeMgr) SID() string { return p.sid }
func (p *cliFakeMgr) Alive() bool { return p.alive.Load() }
func (p *cliFakeMgr) Kill() error { p.alive.Store(false); return nil }
func (p *cliFakeMgr) Wait(ctx context.Context) error {
	_ = ctx
	return nil
}
