package manager

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/journal"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/unit"
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
	for _, name := range []string{DefaultTarget, TimersTarget, ShutdownTarget} {
		if byName[name].Kind != "target" {
			t.Fatalf("builtin %s = %+v", name, byName[name])
		}
	}
	if _, ok := byName["network-online.target"]; ok {
		t.Fatal("network-online.target must not be shipped")
	}
	if _, ok := byName[GraphicalSessionTarget]; ok {
		t.Fatal("system list-units must not show graphical-session.target")
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
	found := false
	for _, u := range list.Units {
		if u.Name == "hermes.service" {
			found = true
			if !u.Enabled {
				t.Fatalf("list after enable: %+v", u)
			}
		}
	}
	if !found {
		t.Fatal("hermes.service missing after enable")
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
	t.Cleanup(func() { stopAll(m) })
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
	if st.Machine == nil || st.Machine.UnitsLoaded != 4 || st.Machine.State != "running" {
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
	if rel.Loaded != 5 {
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

func TestLogsReturnsStoredOutput(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{stdout: "hello from unit\n", stderr: "warn from unit\n"}
	m := managerWith(t, launch, map[string]string{
		"foo.service": `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
	})
	client, stop := serveManager(t, m, protocol.AllowAdmin)
	defer stop()
	ctx := context.Background()
	if _, err := client.Start(ctx, "foo"); err != nil {
		t.Fatal(err)
	}

	var logs *protocol.LogsResult
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		logs, err = client.Logs(ctx, protocol.LogsParams{Unit: "foo"})
		if err != nil {
			t.Fatal(err)
		}
		if len(logs.Entries) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if logs == nil || logs.Unit != "foo.service" || len(logs.Entries) < 2 {
		t.Fatalf("logs = %+v", logs)
	}
	msgs := map[string]protocol.LogEntry{}
	for _, e := range logs.Entries {
		msgs[e.Message] = e
		if e.PID != 1 {
			t.Fatalf("pid = %d", e.PID)
		}
		if e.Timestamp == "" {
			t.Fatal("missing timestamp")
		}
	}
	if msgs["hello from unit"].Stream != "stdout" {
		t.Fatalf("stdout = %+v", msgs["hello from unit"])
	}
	if msgs["hello from unit"].Severity != journal.SeverityInfo {
		t.Fatalf("stdout severity = %+v", msgs["hello from unit"])
	}
	if msgs["warn from unit"].Stream != "stderr" {
		t.Fatalf("stderr = %+v", msgs["warn from unit"])
	}
	if msgs["warn from unit"].Severity != journal.SeverityErr {
		t.Fatalf("stderr severity = %+v", msgs["warn from unit"])
	}
	if msgs["hello from unit"].Session != "" || msgs["hello from unit"].UserSID != "" {
		t.Fatalf("system-scope origin = %+v", msgs["hello from unit"])
	}
	st, err := client.Status(ctx, "foo")
	if err != nil {
		t.Fatal(err)
	}
	if st.Unit == nil || st.Unit.InvocationID == "" {
		t.Fatalf("status missing InvocationID: %+v", st.Unit)
	}
	for _, e := range logs.Entries {
		if e.InvocationID != st.Unit.InvocationID {
			t.Fatalf("journal invocation %q != status %q", e.InvocationID, st.Unit.InvocationID)
		}
	}

	if _, err := client.DaemonReload(ctx); err != nil {
		t.Fatal(err)
	}
	again, err := client.Logs(ctx, protocol.LogsParams{Unit: "foo.service"})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Entries) < 2 {
		t.Fatalf("journal must survive daemon-reload: %+v", again)
	}

	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	none, err := client.Logs(ctx, protocol.LogsParams{Unit: "foo", Since: future})
	if err != nil {
		t.Fatal(err)
	}
	if len(none.Entries) != 0 {
		t.Fatalf("since future = %+v", none)
	}
	past, err := client.Logs(ctx, protocol.LogsParams{Unit: "foo", Since: "1 hour ago"})
	if err != nil {
		t.Fatal(err)
	}
	if len(past.Entries) < 2 {
		t.Fatalf("since 1 hour ago dropped live lines: %+v", past)
	}
	_, err = client.Logs(ctx, protocol.LogsParams{Unit: "foo", Since: "not-a-time"})
	if err == nil {
		t.Fatal("invalid since must not be ignored")
	}
	pe, ok := err.(*protocol.Error)
	if !ok || pe.Code != protocol.CodeInvalidParams {
		t.Fatalf("invalid since err = %v", err)
	}
	if logs.Cursor == "" {
		t.Fatal("logs result missing cursor")
	}
	replay, err := client.Logs(ctx, protocol.LogsParams{Unit: "foo", Cursor: logs.Cursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.Entries) != 0 {
		t.Fatalf("cursor replay = %+v", replay)
	}
}

func TestLogsReadsHistoricalV1(t *testing.T) {
	t.Parallel()
	m := testManager(t, map[string]string{
		"foo.service": `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
	})
	line := `{"v":1,"timestamp":"2026-01-01T00:00:00Z","unit":"foo.service","pid":1,"stream":"stdout","message":"legacy","invocationId":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}` + "\n"
	if err := os.WriteFile(filepath.Join(m.cfg.JournalDir(), "foo.service.log"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	logs, err := m.Logs(protocol.LogsParams{Unit: "foo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(logs.Entries) != 1 || logs.Entries[0].Message != "legacy" || logs.Entries[0].Stream != "stdout" {
		t.Fatalf("v=1 logs = %+v", logs)
	}
	if logs.Entries[0].Severity != "" || logs.Entries[0].Session != "" || logs.Entries[0].UserSID != "" {
		t.Fatalf("v=1 new fields must be empty: %+v", logs.Entries[0])
	}
}

func TestLogsUserScopeSIDAndSession(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{stdout: "hello from user\n", stderr: "warn from user\n"}
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
	const sid = "S-1-5-21-1-2-3-1001"
	m, err := New(Config{
		BaseDir:               dir,
		Launch:                launch,
		UserScope:             true,
		NotifySID:             sid,
		SessionID:             func() string { return "3" },
		HasInteractiveSession: func() bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })

	if _, err := m.Start(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	var logs *protocol.LogsResult
	for time.Now().Before(deadline) {
		logs, err = m.Logs(protocol.LogsParams{Unit: "foo"})
		if err != nil {
			t.Fatal(err)
		}
		if len(logs.Entries) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if logs == nil || len(logs.Entries) < 2 {
		t.Fatalf("user-scope logs = %+v", logs)
	}
	for _, e := range logs.Entries {
		if e.UserSID != sid || e.Session != "3" {
			t.Fatalf("origin = %+v", e)
		}
	}
	msgs := map[string]protocol.LogEntry{}
	for _, e := range logs.Entries {
		msgs[e.Message] = e
	}
	if msgs["hello from user"].Severity != journal.SeverityInfo {
		t.Fatalf("stdout = %+v", msgs["hello from user"])
	}
	if msgs["warn from user"].Severity != journal.SeverityErr {
		t.Fatalf("stderr = %+v", msgs["warn from user"])
	}
}

func TestLogsFollowWaitsForNewLine(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{stdout: "hello from unit\n"}
	m := managerWith(t, launch, map[string]string{
		"foo.service": `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
	})
	type result struct {
		got *protocol.LogsResult
		err error
	}
	ch := make(chan result, 1)
	go func() {
		got, err := m.Logs(protocol.LogsParams{Unit: "foo", Follow: true})
		ch <- result{got, err}
	}()
	time.Sleep(80 * time.Millisecond)
	if _, err := m.Start(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.got == nil || len(r.got.Entries) == 0 {
			t.Fatalf("follow = %+v", r.got)
		}
		found := false
		for _, e := range r.got.Entries {
			if e.Message == "hello from unit" {
				found = true
			}
		}
		if !found {
			t.Fatalf("follow missing line: %+v", r.got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Follow did not return a new line")
	}
}

func TestLogsSinceUsesManagerClock(t *testing.T) {
	t.Parallel()
	m, fk := managerWithFake(t, &fakeLauncher{}, map[string]string{
		"foo.service": `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
	})
	now := fk.Now()
	path := filepath.Join(m.journal.Dir(), "foo.service.log")
	var body []byte
	for _, rec := range []struct {
		ts  time.Time
		msg string
	}{
		{now.Add(-2 * time.Hour), "old"},
		{now, "now"},
	} {
		raw, err := json.Marshal(map[string]any{
			"v":         1,
			"timestamp": rec.ts.UTC().Format(time.RFC3339Nano),
			"unit":      "foo.service",
			"message":   rec.msg,
		})
		if err != nil {
			t.Fatal(err)
		}
		body = append(body, raw...)
		body = append(body, '\n')
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := m.Logs(protocol.LogsParams{Unit: "foo", Since: "1 hour ago"})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got.Entries) != 1 || got.Entries[0].Message != "now" {
		t.Fatalf("since on fake clock = %+v (now=%v)", got, now)
	}
}

func TestLogsFollowDeadlineUsesManagerClock(t *testing.T) {
	t.Parallel()
	m, fk := managerWithFake(t, &fakeLauncher{}, map[string]string{
		"foo.service": `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
	})
	type result struct {
		got *protocol.LogsResult
		err error
	}
	ch := make(chan result, 1)
	go func() {
		got, err := m.Logs(protocol.LogsParams{Unit: "foo", Follow: true})
		ch <- result{got, err}
	}()
	waitCond(t, func() bool { return fk.WaitingAt(journal.FollowPoll()) })
	select {
	case r := <-ch:
		t.Fatalf("Follow returned before clock Advanced: %+v %v", r.got, r.err)
	default:
	}
	fk.Advance(journal.FollowWait())
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.got == nil || len(r.got.Entries) != 0 {
			t.Fatalf("empty follow = %+v", r.got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Follow did not honor manager clock deadline")
	}
}

func TestStatusAndListShowActiveEnabledPID(t *testing.T) {
	t.Parallel()
	m := testManager(t, map[string]string{
		"foo.service": `
[Unit]
Description=Foo
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
[Install]
WantedBy=default.target
`,
	})
	client, stop := serveManager(t, m, protocol.AllowAdmin)
	defer stop()
	ctx := context.Background()

	if _, err := client.Enable(ctx, "foo"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Start(ctx, "foo"); err != nil {
		t.Fatal(err)
	}

	st, err := client.Status(ctx, "foo")
	if err != nil {
		t.Fatal(err)
	}
	if st.Unit == nil || st.Unit.ActiveState != "active" || !st.Unit.Enabled {
		t.Fatalf("status = %+v", st.Unit)
	}
	if st.Unit.MainPID != 1 {
		t.Fatalf("mainPid = %d", st.Unit.MainPID)
	}
	if st.Unit.InvocationID == "" {
		t.Fatal("status missing InvocationID")
	}

	list, err := client.ListUnits(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, u := range list.Units {
		if u.Name != "foo.service" {
			continue
		}
		found = true
		if u.ActiveState != "active" || !u.Enabled || u.MainPID != 1 {
			t.Fatalf("list unit = %+v", u)
		}
	}
	if !found {
		t.Fatal("foo.service missing from list")
	}
}

func TestReloadSkipsUnitWithUnquotedEnvironmentSpaces(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, units, "log.service", `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
Environment=WINUNITD_JOB_PRINT=hello from journal
`)
	m, err := New(Config{BaseDir: dir, Launch: &fakeLauncher{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	rel, err := m.Reload()
	if err != nil {
		t.Fatal(err)
	}
	if len(rel.Errors) == 0 {
		t.Fatal("unquoted Environment value with spaces must produce a reload error")
	}
	_, err = m.Start(context.Background(), "log")
	pe, ok := err.(*protocol.Error)
	if !ok || pe.Code != protocol.CodeNotFound {
		t.Fatalf("start after failed load: %v", err)
	}

	writeUnit(t, units, "log.service", `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
Environment="WINUNITD_JOB_PRINT=hello from journal"
`)
	rel, err = m.Reload()
	if err != nil {
		t.Fatal(err)
	}
	if len(rel.Errors) != 0 {
		t.Fatalf("quoted Environment should load: %v", rel.Errors)
	}
	if _, err := m.Start(context.Background(), "log"); err != nil {
		t.Fatal(err)
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
	m, err := New(Config{BaseDir: dir, Launch: &fakeLauncher{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	return m
}

func stopAll(m *Manager) {
	m.mu.Lock()
	names := append([]string(nil), m.names()...)
	m.mu.Unlock()
	for _, name := range names {
		_, _ = m.Stop(name)
	}
	m.Close()
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

func TestStartPassesExecStartToLauncher(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, units, "foo.service", `
[Service]
Type=simple
ExecStart=C:\Tools\foo.exe
ExecStartArg=--listen
WorkingDirectory=C:\Tools
TimeoutStartSec=30s
`)
	m, err := New(Config{BaseDir: dir, Launch: launch})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	if _, err := m.Start(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
	got := launch.specs()
	if len(got) != 1 {
		t.Fatalf("starts = %+v", got)
	}
	if got[0].Unit != "foo.service" || got[0].Type != unit.TypeSimple {
		t.Fatalf("spec = %+v", got[0])
	}
	if len(got[0].Argv) != 2 || got[0].Argv[0] != `C:\Tools\foo.exe` || got[0].Argv[1] != "--listen" {
		t.Fatalf("argv = %v", got[0].Argv)
	}
	if got[0].Dir != `C:\Tools` || got[0].TimeoutStart != 30*time.Second {
		t.Fatalf("spec = %+v", got[0])
	}
	if got[0].Limits != (runtime.JobLimits{}) {
		t.Fatalf("omitted limits = %+v", got[0].Limits)
	}
}

func TestStartPassesJobLimitsToLauncher(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, units, "cap.service", `
[Service]
Type=simple
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
MemoryMax=2G
ProcessLimit=4
PriorityClass=below-normal
`)
	m, err := New(Config{BaseDir: dir, Launch: launch})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	if _, err := m.Start(context.Background(), "cap"); err != nil {
		t.Fatal(err)
	}
	got := launch.specs()
	if len(got) != 1 {
		t.Fatalf("starts = %+v", got)
	}
	want := runtime.JobLimits{
		MemoryMax:     2 * 1024 * 1024 * 1024,
		ProcessLimit:  4,
		PriorityClass: runtime.PriorityBelowNormal,
	}
	if got[0].Limits != want {
		t.Fatalf("limits = %+v, want %+v", got[0].Limits, want)
	}
}

func TestStartPassesCPUWeightToLauncher(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, units, "cpu.service", `
[Service]
Type=simple
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
CPUWeight=50
IoPriority=low
`)
	m, err := New(Config{BaseDir: dir, Launch: launch})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	if _, err := m.Start(context.Background(), "cpu"); err != nil {
		t.Fatal(err)
	}
	got := launch.specs()
	if len(got) != 1 {
		t.Fatalf("starts = %+v", got)
	}
	want := runtime.JobLimits{
		CPUWeight:     unit.WindowsCPUWeight(50),
		IoPriority:    unit.IoPriorityLowNT,
		IoPrioritySet: true,
	}
	if got[0].Limits != want {
		t.Fatalf("limits = %+v, want %+v", got[0].Limits, want)
	}
}

func TestStartPassesCPUQuotaToLauncher(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, units, "quota.service", `
[Service]
Type=simple
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
CPUQuota=25%
`)
	m, err := New(Config{BaseDir: dir, Launch: launch})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	if _, err := m.Start(context.Background(), "quota"); err != nil {
		t.Fatal(err)
	}
	got := launch.specs()
	if len(got) != 1 {
		t.Fatalf("starts = %+v", got)
	}
	want := runtime.JobLimits{CPURate: unit.WindowsCPURate(25)}
	if got[0].Limits != want {
		t.Fatalf("limits = %+v, want %+v", got[0].Limits, want)
	}
}

func TestStatusResourceLimitReason(t *testing.T) {
	t.Parallel()
	m := testManager(t, map[string]string{
		"cap.service": `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
MemoryMax=2G
`,
	})
	m.mu.Lock()
	if rt := m.units["cap.service"]; rt != nil {
		rt.state = core.Failed
		rt.err = core.ReasonResourceLimit
	}
	m.mu.Unlock()
	st, err := m.Status("cap")
	if err != nil {
		t.Fatal(err)
	}
	if st.Unit == nil || st.Unit.ActiveState != "failed" {
		t.Fatalf("status = %+v", st.Unit)
	}
	if st.Unit.Reason != core.ReasonResourceLimit || st.Unit.Error != core.ReasonResourceLimit {
		t.Fatalf("reason=%q error=%q", st.Unit.Reason, st.Unit.Error)
	}
}

func TestStatusReflectsCPULimits(t *testing.T) {
	t.Parallel()
	m := testManager(t, map[string]string{
		"cpu.service": `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
CPUWeight=50
IoPriority=low
`,
		"quota.service": `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
CPUQuota=25%
`,
	})
	st, err := m.Status("cpu")
	if err != nil {
		t.Fatal(err)
	}
	if st.Unit == nil || st.Unit.CPUWeight != 50 || st.Unit.IoPriority != "low" || st.Unit.CPUQuota != 0 {
		t.Fatalf("cpu status = %+v", st.Unit)
	}
	st, err = m.Status("quota")
	if err != nil {
		t.Fatal(err)
	}
	if st.Unit == nil || st.Unit.CPUQuota != 25 || st.Unit.CPUWeight != 0 {
		t.Fatalf("quota status = %+v", st.Unit)
	}
}

func TestStatusSignalEquivalentReason(t *testing.T) {
	t.Parallel()
	m := testManager(t, map[string]string{
		"crash.service": `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
	})
	m.mu.Lock()
	if rt := m.units["crash.service"]; rt != nil {
		rt.state = core.Failed
		rt.err = core.ReasonSignalEquivalent
	}
	m.mu.Unlock()
	st, err := m.Status("crash")
	if err != nil {
		t.Fatal(err)
	}
	if st.Unit == nil || st.Unit.ActiveState != "failed" {
		t.Fatalf("status = %+v", st.Unit)
	}
	if st.Unit.Reason != core.ReasonSignalEquivalent || st.Unit.Error != core.ReasonSignalEquivalent {
		t.Fatalf("reason=%q error=%q", st.Unit.Reason, st.Unit.Error)
	}
}

func TestStartHonorsGraphOrdering(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, units, "web.service", `
[Unit]
Requires=db.service
After=db.service
[Service]
ExecStart=C:\Tools\web.exe
WorkingDirectory=C:\Tools
`)
	writeUnit(t, units, "db.service", `
[Service]
ExecStart=C:\Tools\db.exe
WorkingDirectory=C:\Tools
`)
	m, err := New(Config{BaseDir: dir, Launch: launch})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	if _, err := m.Start(context.Background(), "web"); err != nil {
		t.Fatal(err)
	}
	got := launch.units()
	if len(got) != 2 || got[0] != "db.service" || got[1] != "web.service" {
		t.Fatalf("start order = %v", got)
	}
}

func TestStartTargetDoesNotLaunch(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, units, "app.target", `
[Unit]
Description=App
`)
	m, err := New(Config{BaseDir: dir, Launch: launch})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	st, err := m.Start(context.Background(), "app.target")
	if err != nil {
		t.Fatal(err)
	}
	if st.ActiveState != "active" {
		t.Fatalf("target = %+v", st)
	}
	if specs := launch.specs(); len(specs) != 0 {
		t.Fatalf("target must not CreateProcess: %+v", specs)
	}
}

type fakeLauncher struct {
	mu     sync.Mutex
	starts []runtime.StartSpec
	stops  []string
	stdout string
	stderr string
	// pid is reported as the unit main PID. Zero means 1 (existing tests).
	pid     int
	firstAt time.Time
}

func (f *fakeLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	f.mu.Lock()
	f.starts = append(f.starts, spec)
	if f.firstAt.IsZero() {
		f.firstAt = time.Now()
	}
	out, errOut := f.stdout, f.stderr
	pid := f.pid
	f.mu.Unlock()
	if pid == 0 {
		pid = 1
	}
	job, err := runtime.OpenUnitJob()
	if err != nil {
		return nil, err
	}
	p := &fakeProc{
		name:   spec.Unit,
		rec:    f,
		pid:    pid,
		job:    job,
		done:   make(chan struct{}),
		stdout: io.NopCloser(strings.NewReader(out)),
		stderr: io.NopCloser(strings.NewReader(errOut)),
	}
	if spec.Type == unit.TypeOneshot {
		p.die(0)
	}
	return p, nil
}

func (f *fakeLauncher) specs() []runtime.StartSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]runtime.StartSpec, len(f.starts))
	copy(out, f.starts)
	return out
}

func (f *fakeLauncher) units() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.starts))
	for i, s := range f.starts {
		out[i] = s.Unit
	}
	return out
}

func (f *fakeLauncher) stopped() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.stops))
	copy(out, f.stops)
	return out
}

type fakeProc struct {
	mu       sync.Mutex
	name     string
	rec      *fakeLauncher
	pid      int
	job      runtime.Job
	dead     bool
	closed   bool
	exitCode uint32
	exited   bool
	done     chan struct{}
	stdout   io.ReadCloser
	stderr   io.ReadCloser
}

func (p *fakeProc) PID() int {
	if p.pid != 0 {
		return p.pid
	}
	return 1
}

func (p *fakeProc) ExitCode() (uint32, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.exited && !p.dead && !p.closed {
		return 0, false
	}
	return p.exitCode, true
}

func (p *fakeProc) Job() runtime.Job { return p.job }

func (p *fakeProc) Stdout() io.ReadCloser { return p.stdout }

func (p *fakeProc) Stderr() io.ReadCloser { return p.stderr }

func (p *fakeProc) Alive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.dead && !p.closed
}

func (p *fakeProc) Wait(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		p.mu.Lock()
		code := p.exitCode
		p.mu.Unlock()
		if code == 0 {
			return nil
		}
		return &runtime.ExitStatus{Code: code}
	}
}

func (p *fakeProc) Stop(timeout time.Duration) error {
	_ = timeout
	if p.rec != nil && p.name != "" {
		p.rec.mu.Lock()
		p.rec.stops = append(p.rec.stops, p.name)
		p.rec.mu.Unlock()
	}
	p.finish()
	if p.job != nil {
		_ = p.job.Kill()
	}
	return p.Close()
}

func (p *fakeProc) finish() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dead = true
	p.exited = true
	select {
	case <-p.done:
	default:
		close(p.done)
	}
}

func (p *fakeProc) Close() error {
	p.finish()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	if p.job != nil {
		_ = p.job.Close()
	}
	if p.stdout != nil {
		_ = p.stdout.Close()
	}
	if p.stderr != nil {
		_ = p.stderr.Close()
	}
	return nil
}
