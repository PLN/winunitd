package manager

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/protocol"
)

func TestListUnitsOverPipe(t *testing.T) {
	t.Parallel()
	m := testManager(t, map[string]string{
		"foo.service": `
[Unit]
Description=Foo
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"bar.timer": `
[Timer]
OnCalendar=daily
`,
		"app.target": `
[Unit]
Description=App
`,
	})
	client, stop := serveManager(t, m, protocol.AllowAdmin)
	defer stop()

	got, err := client.ListUnits(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Units) != 3 {
		t.Fatalf("units = %+v", got.Units)
	}
	byName := map[string]protocol.UnitStatus{}
	for _, u := range got.Units {
		byName[u.Name] = u
	}
	if byName["foo.service"].Description != "Foo" || byName["foo.service"].ActiveState != "inactive" {
		t.Fatalf("foo = %+v", byName["foo.service"])
	}
	if byName["foo.service"].Kind != "service" || byName["bar.timer"].Kind != "timer" {
		t.Fatalf("kinds = %+v", byName)
	}
	if byName["app.target"].Kind != "target" {
		t.Fatalf("target = %+v", byName["app.target"])
	}
}

func TestNonAdminDenied(t *testing.T) {
	t.Parallel()
	m := testManager(t, map[string]string{
		"foo.service": `[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
	})
	client, stop := serveManager(t, m, protocol.DenyAll)
	defer stop()
	_, err := client.ListUnits(context.Background())
	pe, ok := err.(*protocol.Error)
	if !ok || pe.Code != protocol.CodePermissionDenied {
		t.Fatalf("err = %v", err)
	}
}

func TestStartStopRestartThroughProtocol(t *testing.T) {
	t.Parallel()
	m := testManager(t, map[string]string{
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
	client, stop := serveManager(t, m, protocol.AllowAdmin)
	defer stop()
	ctx := context.Background()

	st, err := client.Start(ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	if st.Unit != "web.service" || st.ActiveState != "active" {
		t.Fatalf("start = %+v", st)
	}

	list, err := client.ListUnits(ctx)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, u := range list.Units {
		states[u.Name] = u.ActiveState
	}
	if states["web.service"] != "active" || states["db.service"] != "active" {
		t.Fatalf("after start: %v", states)
	}

	stopped, err := client.Stop(ctx, "web.service")
	if err != nil {
		t.Fatal(err)
	}
	if stopped.ActiveState != "inactive" {
		t.Fatalf("stop = %+v", stopped)
	}

	restarted, err := client.Restart(ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	if restarted.ActiveState != "active" {
		t.Fatalf("restart = %+v", restarted)
	}
}

func TestEnableDisable(t *testing.T) {
	t.Parallel()
	m := testManager(t, map[string]string{
		"hermes.service": `
[Unit]
Description=Hermes
[Service]
ExecStart=C:\Tools\hermes.exe
WorkingDirectory=C:\Tools
[Install]
WantedBy=default.target
`,
	})
	client, stop := serveManager(t, m, protocol.AllowAdmin)
	defer stop()
	ctx := context.Background()

	en, err := client.Enable(ctx, "hermes")
	if err != nil {
		t.Fatal(err)
	}
	if !en.Enabled || len(en.Targets) != 1 || en.Targets[0] != "default.target" {
		t.Fatalf("enable = %+v", en)
	}
	link := filepath.Join(m.cfg.EnabledDir(), "default.target", "hermes.service")
	if _, err := os.Stat(link); err != nil {
		t.Fatalf("enable file: %v", err)
	}

	list, err := client.ListUnits(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !list.Units[0].Enabled {
		t.Fatalf("list after enable: %+v", list.Units[0])
	}

	dis, err := client.Disable(ctx, "hermes.service")
	if err != nil {
		t.Fatal(err)
	}
	if dis.Enabled {
		t.Fatalf("disable = %+v", dis)
	}
	if _, err := os.Stat(link); !os.IsNotExist(err) {
		t.Fatalf("enable file still present: %v", err)
	}
}

func TestDaemonReloadAndStatus(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, units, "foo.service", `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`)
	m, err := New(Config{BaseDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	client, stop := serveManager(t, m, protocol.AllowAdmin)
	defer stop()
	ctx := context.Background()

	st, err := client.Status(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if st.Machine == nil || st.Machine.UnitsLoaded != 1 || st.Machine.State != "running" {
		t.Fatalf("machine = %+v", st.Machine)
	}

	writeUnit(t, units, "bar.service", `
[Unit]
Description=Bar
[Service]
ExecStart=C:\Tools\bar.exe
WorkingDirectory=C:\Tools
`)
	rel, err := client.DaemonReload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rel.Loaded != 2 {
		t.Fatalf("reload = %+v", rel)
	}

	ust, err := client.Status(ctx, "bar")
	if err != nil {
		t.Fatal(err)
	}
	if ust.Unit == nil || ust.Unit.Description != "Bar" {
		t.Fatalf("unit status = %+v", ust.Unit)
	}
}

func TestLogsAndVerifyOverPipe(t *testing.T) {
	t.Parallel()
	m := testManager(t, map[string]string{
		"foo.service": `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
	})
	client, stop := serveManager(t, m, protocol.AllowAdmin)
	defer stop()
	ctx := context.Background()

	logs, err := client.Logs(ctx, protocol.LogsParams{Unit: "foo"})
	if err != nil {
		t.Fatal(err)
	}
	if logs.Unit != "foo.service" || logs.Entries == nil || len(logs.Entries) != 0 {
		t.Fatalf("logs = %+v", logs)
	}

	ver, err := client.Verify(ctx, "foo.service")
	if err != nil {
		t.Fatal(err)
	}
	if !ver.OK || ver.Name != "foo.service" {
		t.Fatalf("verify = %+v", ver)
	}
}

func TestListTimers(t *testing.T) {
	t.Parallel()
	m := testManager(t, map[string]string{
		"foo.service": `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.timer": `
[Timer]
OnCalendar=daily
`,
	})
	client, stop := serveManager(t, m, protocol.AllowAdmin)
	defer stop()
	got, err := client.ListTimers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Timers) != 1 || got.Timers[0].Name != "foo.timer" || got.Timers[0].Unit != "foo.service" {
		t.Fatalf("timers = %+v", got.Timers)
	}
}

func TestStartMissingUnit(t *testing.T) {
	t.Parallel()
	m := testManager(t, map[string]string{})
	client, stop := serveManager(t, m, protocol.AllowAdmin)
	defer stop()
	_, err := client.Start(context.Background(), "nope")
	pe, ok := err.(*protocol.Error)
	if !ok || pe.Code != protocol.CodeNotFound {
		t.Fatalf("err = %v", err)
	}
}

func TestStartEmptyName(t *testing.T) {
	t.Parallel()
	m := testManager(t, map[string]string{
		"foo.service": `[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
	})
	client, stop := serveManager(t, m, protocol.AllowAdmin)
	defer stop()
	err := client.Call(context.Background(), protocol.MethodStart, protocol.UnitParams{Unit: ""}, nil)
	pe, ok := err.(*protocol.Error)
	if !ok || pe.Code != protocol.CodeInvalidParams {
		t.Fatalf("err = %v", err)
	}
}

func TestAllElevenVerbsOnManager(t *testing.T) {
	t.Parallel()
	m := testManager(t, map[string]string{
		"foo.service": `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
[Install]
WantedBy=default.target
`,
		"foo.timer": `
[Timer]
OnCalendar=daily
`,
	})
	client, stop := serveManager(t, m, protocol.AllowAdmin)
	defer stop()
	ctx := context.Background()
	must := func(method string, err error) {
		t.Helper()
		if err != nil {
			t.Errorf("%s: %v", method, err)
		}
	}
	_, err := client.ListUnits(ctx)
	must(protocol.MethodListUnits, err)
	_, err = client.ListTimers(ctx)
	must(protocol.MethodListTimers, err)
	_, err = client.Status(ctx, "foo")
	must(protocol.MethodStatus, err)
	_, err = client.Start(ctx, "foo")
	must(protocol.MethodStart, err)
	_, err = client.Stop(ctx, "foo")
	must(protocol.MethodStop, err)
	_, err = client.Restart(ctx, "foo")
	must(protocol.MethodRestart, err)
	_, err = client.Enable(ctx, "foo")
	must(protocol.MethodEnable, err)
	_, err = client.Disable(ctx, "foo")
	must(protocol.MethodDisable, err)
	_, err = client.Logs(ctx, protocol.LogsParams{Unit: "foo"})
	must(protocol.MethodLogs, err)
	_, err = client.DaemonReload(ctx)
	must(protocol.MethodDaemonReload, err)
	_, err = client.Verify(ctx, "foo")
	must(protocol.MethodVerify, err)
}

func TestProtocolVersionOnManager(t *testing.T) {
	t.Parallel()
	m := testManager(t, nil)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = protocol.Serve(ctx, lis, m, protocol.AllowAdmin) }()

	conn, err := net.Dial(lis.Addr().Network(), lis.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(`{"protocol":"winunitd.control","version":2,"id":1,"method":"list-units"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	var resp protocol.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil || resp.Error.Code != protocol.CodeProtocolMismatch {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Version != protocol.Version {
		t.Fatalf("version = %d", resp.Version)
	}
}

func testManager(t *testing.T, files map[string]string) *Manager {
	t.Helper()
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		writeUnit(t, units, name, body)
	}
	m, err := New(Config{BaseDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	return m
}

func writeUnit(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func serveManager(t *testing.T, m *Manager, auth protocol.Authorizer) (*protocol.Client, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() { errc <- protocol.Serve(ctx, lis, m, auth) }()
	var d net.Dialer
	conn, err := d.DialContext(ctx, lis.Addr().Network(), lis.Addr().String())
	if err != nil {
		cancel()
		lis.Close()
		t.Fatal(err)
	}
	stop := func() {
		cancel()
		_ = conn.Close()
		_ = lis.Close()
		select {
		case <-errc:
		case <-time.After(2 * time.Second):
		}
	}
	return protocol.NewClient(conn), stop
}
