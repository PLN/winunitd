//go:build windows

package main

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/manager"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/runtime/runtimetest"
)

func TestWindowsGraphicalSessionTargetAfterSimulatedLogon(t *testing.T) {
	pipeName := newTestUserPipe()
	sid, err := protocol.CurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	info, err := runtime.CurrentUserInfo()
	if err != nil {
		t.Fatal(err)
	}

	userDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(userDir, "units"), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(userDir, "mgr.log")
	logf, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logf.Close()

	h := manager.NewUserHost(manager.UserHostConfig{
		Exe:       os.Args[0],
		ExtraArgs: []string{"--base-dir", userDir},
		QueryToken: func(sessionID uint32) (*runtime.UserToken, error) {
			return &runtime.UserToken{Info: info}, nil
		},
		Start: func(spec runtime.UserManagerSpec) (runtime.UserManagerProc, error) {
			args := runtime.UserManagerArgs(spec.SID, spec.ExtraArgs)
			cmd := exec.Command(spec.Exe, args...)
			cmd.Env = append(append([]string{}, spec.Env...), "WINUNITD_TEST_DAEMON=1", "WINUNITD_TEST_USER_PIPE="+pipeName)
			cmd.Stdout = logf
			cmd.Stderr = logf
			if err := cmd.Start(); err != nil {
				return nil, err
			}
			p := &execUserProc{cmd: cmd, sid: spec.SID, done: make(chan struct{})}
			go func() {
				_ = cmd.Wait()
				close(p.done)
			}()
			return p, nil
		},
		Sessions: func() ([]uint32, error) { return nil, nil },
	})
	t.Cleanup(h.Close)

	h.Logon(1)
	if !h.Alive(sid) {
		t.Fatalf("user manager did not start; log:\n%s", readFile(t, logPath))
	}

	userDial := func(ctx context.Context) (net.Conn, error) {
		return protocol.DialPipe(ctx, pipeName)
	}
	waitUserPipe(t, userDial)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := userDial(ctx)
	if err != nil {
		t.Fatalf("dial: %v; log:\n%s", err, readFile(t, logPath))
	}
	defer conn.Close()
	cl := protocol.NewClient(conn)

	list, err := cl.ListUnits(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, u := range list.Units {
		if u.Name == manager.GraphicalSessionTarget {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("list-units missing graphical-session.target: %+v; log:\n%s", list.Units, readFile(t, logPath))
	}

	st, err := cl.Status(ctx, manager.GraphicalSessionTarget)
	if err != nil {
		t.Fatal(err)
	}
	if st.Unit == nil || st.Unit.Name != manager.GraphicalSessionTarget {
		t.Fatalf("status = %+v", st.Unit)
	}

	want := core.Inactive.String()
	if runtime.SIDHasInteractiveSession(sid) {
		want = core.Active.String()
	}
	deadline := time.Now().Add(5 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		st, err = cl.Status(ctx, manager.GraphicalSessionTarget)
		if err != nil {
			t.Fatal(err)
		}
		got = st.Unit.ActiveState
		if got == want {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got != want {
		t.Fatalf("graphical-session.target ActiveState = %s, want %s (SIDHasInteractiveSession=%v); log:\n%s",
			got, want, runtime.SIDHasInteractiveSession(sid), readFile(t, logPath))
	}

	h.Logoff(1)
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !h.Alive(sid) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if h.Alive(sid) {
		t.Fatal("simulated logoff must tear down the user manager")
	}
}

func TestWindowsGraphicalSessionInjectedLogonLogoffAndLinger(t *testing.T) {
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(units, "linger.service"), []byte(`
[Service]
ExecStart=C:\Windows\System32\hostname.exe
WorkingDirectory=C:\Windows\System32
[Install]
WantedBy=default.target
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(units, "gui.service"), []byte(`
[Unit]
RequiresInteractiveSession=yes
[Service]
Type=oneshot
ExecStart=C:\Windows\System32\hostname.exe
WorkingDirectory=C:\Windows\System32
`), 0o644); err != nil {
		t.Fatal(err)
	}

	has := false
	m, err := manager.New(manager.Config{
		BaseDir:               dir,
		Launch:                runtimetest.Launcher(),
		UserScope:             true,
		HasInteractiveSession: func() bool { return has },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Enable("linger.service"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.SyncGraphicalSession(context.Background()); err != nil {
		t.Fatal(err)
	}

	st, err := m.Status(manager.GraphicalSessionTarget)
	if err != nil {
		t.Fatal(err)
	}
	if st.Unit == nil || st.Unit.ActiveState != core.Inactive.String() {
		t.Fatalf("linger-without-session: %+v", st.Unit)
	}

	res, err := m.Start(context.Background(), "gui.service")
	if err != nil {
		t.Fatalf("skip must not fail: %v", err)
	}
	if res.ActiveState != core.Inactive.String() {
		t.Fatalf("RequiresInteractiveSession while target down: %s", res.ActiveState)
	}
	gui, err := m.Status("gui")
	if err != nil {
		t.Fatal(err)
	}
	if gui.Unit != nil && gui.Unit.MainPID != 0 {
		t.Fatal("headless RequiresInteractiveSession unit must not have a process")
	}
	linger, err := m.Status("linger.service")
	if err != nil {
		t.Fatal(err)
	}
	if linger.Unit == nil || linger.Unit.ActiveState != core.Active.String() {
		t.Fatalf("linger unit should keep running: %+v", linger.Unit)
	}

	has = true
	if err := m.SyncGraphicalSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, err = m.Status(manager.GraphicalSessionTarget)
	if err != nil {
		t.Fatal(err)
	}
	if st.Unit == nil || st.Unit.ActiveState != core.Active.String() {
		t.Fatalf("after simulated logon: %+v", st.Unit)
	}

	has = false
	if err := m.SyncGraphicalSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, err = m.Status(manager.GraphicalSessionTarget)
	if err != nil {
		t.Fatal(err)
	}
	if st.Unit == nil || st.Unit.ActiveState != core.Inactive.String() {
		t.Fatalf("after last logoff: %+v", st.Unit)
	}
	linger, err = m.Status("linger.service")
	if err != nil {
		t.Fatal(err)
	}
	if linger.Unit == nil || linger.Unit.ActiveState != core.Active.String() {
		t.Fatalf("linger unit must keep running after logoff: %+v", linger.Unit)
	}
}
