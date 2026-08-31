package manager

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
)

func testLingerHost(t *testing.T) (*UserHost, *LingerStore) {
	t.Helper()
	dir := t.TempDir()
	h, _, _ := testUserHost(t, map[uint32]string{1: testSIDA, 2: testSIDB}, nil)
	h.cfg.LingerDir = dir
	h.store = OpenLingerStore(dir)
	h.cfg.Lookup = func(name string) (runtime.UserInfo, error) {
		if name == testSIDA || name == "ferdinand" || name == `TEST\ferd` {
			return runtime.UserInfo{SID: testSIDA, Username: "ferd", Domain: "TEST", Profile: `C:\Users\ferd`}, nil
		}
		if name == testSIDB {
			return runtime.UserInfo{SID: testSIDB, Username: "other", Domain: "TEST"}, nil
		}
		return runtime.UserInfo{}, os.ErrNotExist
	}
	h.cfg.LingerToken = func(rec runtime.LingerRecord) (*runtime.UserToken, error) {
		return &runtime.UserToken{Info: runtime.UserInfo{
			SID:      rec.SID,
			Username: "ferd",
			Domain:   "TEST",
			Profile:  `C:\Users\ferd`,
		}}, nil
	}
	return h, h.store
}

func TestLingerStoreRoundTrip(t *testing.T) {
	t.Parallel()
	s := OpenLingerStore(t.TempDir())
	rec := runtime.LingerRecord{SID: testSIDA, Name: `TEST\ferd`, CredentialURI: "credman://winunitd/linger/" + testSIDA}
	if err := s.Put(rec); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(testSIDA)
	if err != nil {
		t.Fatal(err)
	}
	if got.SID != rec.SID || got.Name != rec.Name || got.CredentialURI != rec.CredentialURI {
		t.Fatalf("got %+v", got)
	}
	if !s.Has(testSIDA) {
		t.Fatal("Has")
	}
	data, err := os.ReadFile(filepath.Join(s.dir, testSIDA))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(data)), "password") {
		t.Fatalf("linger file contains password: %s", data)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["password"]; ok {
		t.Fatal("password field")
	}
}

func TestLingerStoreRejectsPasswordField(t *testing.T) {
	t.Parallel()
	s := OpenLingerStore(t.TempDir())
	path := filepath.Join(s.dir, testSIDA)
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"sid":"`+testSIDA+`","password":"hunter2"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(testSIDA); err == nil {
		t.Fatal("password field must be rejected")
	}
}

func TestEnableLingerStartsManagerWithoutSession(t *testing.T) {
	t.Parallel()
	h, _ := testLingerHost(t)
	got, err := h.EnableLinger("ferdinand")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Lingering || got.SID != testSIDA {
		t.Fatalf("result = %+v", got)
	}
	if !h.Alive(testSIDA) {
		t.Fatal("enable-linger must start a manager with no session")
	}
	if !h.Lingering(testSIDA) {
		t.Fatal("linger record missing")
	}
}

func TestLingerSurvivesLogoff(t *testing.T) {
	t.Parallel()
	h, _ := testLingerHost(t)
	h.Logon(1)
	if _, err := h.EnableLinger(testSIDA); err != nil {
		t.Fatal(err)
	}
	h.Logoff(1)
	if !h.Alive(testSIDA) {
		t.Fatal("logoff must not kill a lingering manager")
	}
}

func TestStartLingeringAtBootNoSession(t *testing.T) {
	t.Parallel()
	h, store := testLingerHost(t)
	if err := store.Put(runtime.LingerRecord{SID: testSIDA, Name: "ferd"}); err != nil {
		t.Fatal(err)
	}
	h.StartLingering()
	if !h.Alive(testSIDA) {
		t.Fatal("boot must start lingering managers without a session")
	}
}

func TestDisableLingerKillsWhenNoSession(t *testing.T) {
	t.Parallel()
	h, _ := testLingerHost(t)
	if _, err := h.EnableLinger(testSIDA); err != nil {
		t.Fatal(err)
	}
	if _, err := h.DisableLinger(testSIDA); err != nil {
		t.Fatal(err)
	}
	if h.Alive(testSIDA) {
		t.Fatal("disable-linger with no session must kill the manager")
	}
	if h.Lingering(testSIDA) {
		t.Fatal("linger record should be gone")
	}
}

func TestDisableLingerKeepsLoggedOnManager(t *testing.T) {
	t.Parallel()
	h, _ := testLingerHost(t)
	h.Logon(1)
	if _, err := h.EnableLinger(testSIDA); err != nil {
		t.Fatal(err)
	}
	if _, err := h.DisableLinger(testSIDA); err != nil {
		t.Fatal(err)
	}
	if !h.Alive(testSIDA) {
		t.Fatal("still logged on: keep P1 behavior")
	}
	h.Logoff(1)
	if h.Alive(testSIDA) {
		t.Fatal("after disable-linger, last logoff must kill the manager")
	}
}

func TestControlEnableLingerAdmin(t *testing.T) {
	t.Parallel()
	h, _ := testLingerHost(t)
	sys := testManager(t, map[string]string{
		"system.service": `
[Service]
ExecStart=C:\Tools\system.exe
WorkingDirectory=C:\Tools
`,
	})
	ctrl := &Control{Units: sys, Users: h}
	client, stop := serveControl(t, ctrl, protocol.AllowAdmin)
	defer stop()
	got, err := client.EnableLinger(context.Background(), "ferdinand")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Lingering {
		t.Fatalf("got %+v", got)
	}
	if !h.Alive(testSIDA) {
		t.Fatal("manager should be running")
	}
}

func TestControlEnableLingerOwnerDenied(t *testing.T) {
	t.Parallel()
	h, _ := testLingerHost(t)
	sys := testManager(t, map[string]string{
		"system.service": `
[Service]
ExecStart=C:\Tools\system.exe
WorkingDirectory=C:\Tools
`,
	})
	ctrl := &Control{Units: sys, Users: h}
	client, stop := serveControl(t, ctrl, protocol.AllowOwner)
	defer stop()
	_, err := client.EnableLinger(context.Background(), "ferdinand")
	pe, ok := err.(*protocol.Error)
	if !ok || pe.Code != protocol.CodePermissionDenied {
		t.Fatalf("err = %v", err)
	}
	if h.Lingering(testSIDA) {
		t.Fatal("non-admin must not write linger state")
	}
}

func TestRequiresInteractiveSessionSkipsHeadless(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(units, "gui.service"), []byte(`
[Unit]
RequiresInteractiveSession=yes
[Service]
Type=oneshot
ExecStart=C:\Tools\gui.exe
WorkingDirectory=C:\Tools
`), 0o644); err != nil {
		t.Fatal(err)
	}
	launch := &fakeLauncher{}
	m, err := New(Config{
		BaseDir:               dir,
		Launch:                launch,
		HasInteractiveSession: func() bool { return false },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	res, err := m.Start(context.Background(), "gui.service")
	if err != nil {
		t.Fatalf("skip should not fail the RPC: %v", err)
	}
	if res.ActiveState != core.Inactive.String() {
		t.Fatalf("state = %s, want inactive", res.ActiveState)
	}
	if len(launch.specs()) != 0 {
		t.Fatal("headless manager must not start RequiresInteractiveSession=yes")
	}
}

func TestRequiresInteractiveSessionStartsWithSession(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(units, "gui.service"), []byte(`
[Unit]
RequiresInteractiveSession=yes
[Service]
Type=oneshot
ExecStart=C:\Tools\gui.exe
WorkingDirectory=C:\Tools
`), 0o644); err != nil {
		t.Fatal(err)
	}
	launch := &fakeLauncher{}
	m, err := New(Config{
		BaseDir:               dir,
		Launch:                launch,
		HasInteractiveSession: func() bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	res, err := m.Start(context.Background(), "gui.service")
	if err != nil {
		t.Fatal(err)
	}
	if res.ActiveState != core.Active.String() {
		t.Fatalf("state = %s", res.ActiveState)
	}
	if len(launch.specs()) != 1 {
		t.Fatal("interactive session should start the unit")
	}
}

func TestManagerHandleDoesNotImplementLinger(t *testing.T) {
	t.Parallel()
	m := testManager(t, map[string]string{
		"system.service": `
[Service]
ExecStart=C:\Tools\system.exe
WorkingDirectory=C:\Tools
`,
	})
	_, err := m.Handle(context.Background(), protocol.MethodEnableLinger, json.RawMessage(`{"user":"ferd"}`))
	if err == nil {
		t.Fatal("user/unit manager must not implement linger verbs")
	}
}

func serveControl(t *testing.T, h protocol.Handler, auth protocol.Authorizer) (*protocol.Client, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() { errc <- protocol.Serve(ctx, lis, h, auth) }()
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
