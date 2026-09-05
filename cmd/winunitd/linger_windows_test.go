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

func TestWindowsLingerBootNoSessionAndLogoff(t *testing.T) {
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
	sysDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sysDir, "units"), 0o755); err != nil {
		t.Fatal(err)
	}
	lingerDir := filepath.Join(sysDir, "linger")

	logPath := filepath.Join(userDir, "mgr.log")
	logf, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logf.Close()

	h := manager.NewUserHost(manager.UserHostConfig{
		Exe:       os.Args[0],
		ExtraArgs: []string{"--base-dir", userDir},
		LingerDir: lingerDir,
		QueryToken: func(sessionID uint32) (*runtime.UserToken, error) {
			return &runtime.UserToken{Info: info}, nil
		},
		LingerToken: func(rec runtime.LingerRecord) (*runtime.UserToken, error) {
			return &runtime.UserToken{Info: info}, nil
		},
		Lookup: func(name string) (runtime.UserInfo, error) {
			return info, nil
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

	got, err := h.EnableLinger(sid)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Lingering {
		t.Fatalf("result = %+v", got)
	}
	if !h.Alive(sid) {
		t.Fatalf("enable-linger did not start a manager; log:\n%s", readFile(t, logPath))
	}

	// Simulate boot: new host, linger record on disk, no session.
	h.Close()
	h2 := manager.NewUserHost(manager.UserHostConfig{
		Exe:       os.Args[0],
		ExtraArgs: []string{"--base-dir", userDir},
		LingerDir: lingerDir,
		LingerToken: func(rec runtime.LingerRecord) (*runtime.UserToken, error) {
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
		QueryToken: func(sessionID uint32) (*runtime.UserToken, error) {
			return &runtime.UserToken{Info: info}, nil
		},
	})
	t.Cleanup(h2.Close)
	h2.Reconcile()
	h2.StartLingering()
	if !h2.Alive(sid) {
		t.Fatalf("boot did not start lingering manager; log:\n%s", readFile(t, logPath))
	}

	h2.Logon(1)
	h2.Logoff(1)
	if !h2.Alive(sid) {
		t.Fatal("logoff must not kill a lingering manager")
	}

	userDial := func(ctx context.Context) (net.Conn, error) {
		return protocol.DialPipe(ctx, pipeName)
	}
	waitUserPipe(t, userDial)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := userDial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	list, err := protocol.NewClient(conn).ListUnits(ctx)
	_ = conn.Close()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	inactive := false
	for _, u := range list.Units {
		if u.Name == manager.GraphicalSessionTarget {
			found = true
			inactive = u.ActiveState == "inactive"
			break
		}
	}
	if !found {
		t.Fatal("lingering user manager must list graphical-session.target")
	}
	if !runtime.SIDHasInteractiveSession(sid) && !inactive {
		t.Fatal("linger-without-session must not activate graphical-session.target")
	}
}

func TestWindowsRequiresInteractiveSessionHeadless(t *testing.T) {
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
ExecStart=C:\Windows\System32\hostname.exe
WorkingDirectory=C:\Windows\System32
`), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := manager.New(manager.Config{
		BaseDir:               dir,
		HasInteractiveSession: func() bool { return false },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	res, err := m.Start(context.Background(), "gui")
	if err != nil {
		t.Fatalf("skip must not fail: %v", err)
	}
	if res.ActiveState != core.Inactive.String() {
		t.Fatalf("state = %s", res.ActiveState)
	}
	st, err := m.Status("gui")
	if err != nil {
		t.Fatal(err)
	}
	if st.Unit != nil && st.Unit.MainPID != 0 {
		t.Fatal("headless RequiresInteractiveSession unit must not have a process")
	}
}

func TestWindowsNonAdminEnableLingerDenied(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "units"), 0o755); err != nil {
		t.Fatal(err)
	}
	m, err := manager.New(manager.Config{BaseDir: dir, Launch: runtimetest.Launcher()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	h := manager.NewUserHost(manager.UserHostConfig{
		Exe:       "winunitd-test",
		LingerDir: filepath.Join(dir, "linger"),
		Start: func(spec runtime.UserManagerSpec) (runtime.UserManagerProc, error) {
			t.Fatal("non-admin must not start a manager")
			return nil, nil
		},
	})
	t.Cleanup(h.Close)
	ctrl := &manager.Control{Units: m, Users: h}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	go protocol.Serve(ctx, lis, ctrl, protocol.AllowOwner)

	var d net.Dialer
	conn, err := d.DialContext(ctx, lis.Addr().Network(), lis.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, err = protocol.NewClient(conn).EnableLinger(ctx, "alice")
	pe, ok := err.(*protocol.Error)
	if !ok || pe.Code != protocol.CodePermissionDenied {
		t.Fatalf("err = %v", err)
	}
}
