package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
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
		cancel()
		_ = lis.Close()
		select {
		case <-errc:
		case <-time.After(2 * time.Second):
		}
	}
	return m, dial, stop
}
