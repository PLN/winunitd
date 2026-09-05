//go:build windows

package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/manager"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/runtime/runtimetest"
)

func TestMain(m *testing.M) {
	if path := os.Getenv("WINUNITD_USER_ONESHOT"); path != "" {
		f, err := os.Create(path)
		if err != nil {
			os.Exit(1)
		}
		fmt.Fprintf(f, "USER=%s\n", os.Getenv("USERNAME"))
		fmt.Fprintf(f, "PROFILE=%s\n", os.Getenv("USERPROFILE"))
		fmt.Fprintf(f, "LOCAL=%s\n", os.Getenv("LOCALAPPDATA"))
		_ = f.Close()
		os.Exit(0)
	}
	if os.Getenv("WINUNITD_TEST_DAEMON") == "1" {
		name := os.Getenv("WINUNITD_TEST_USER_PIPE")
		if !strings.HasPrefix(name, testUserPipePrefix) || len(name) == len(testUserPipePrefix) || len(os.Args) < 3 || os.Args[1] != "--user-manager" {
			fmt.Fprintln(os.Stderr, "test daemon requires an isolated user endpoint and user-manager mode")
			os.Exit(2)
		}
		listenUserControl = func(sid string) (net.Listener, error) {
			sddl, err := protocol.UserPipeSDDL(sid)
			if err != nil {
				return nil, err
			}
			return protocol.ListenPipeSDDL(name, sddl)
		}
		os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

const testUserPipePrefix = `\\.\pipe\winunitd-test\user\`

func newTestUserPipe() string {
	return testUserPipePrefix + rand.Text()
}

func TestTestDaemonRejectsProductionEndpoints(t *testing.T) {
	for _, name := range []string{"", protocol.DefaultPipeName, protocol.UserPipeName("S-1-5-21-1-2-3-1001"), testUserPipePrefix} {
		t.Run(fmt.Sprintf("endpoint-%d", len(name)), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			base := t.TempDir()
			cmd := exec.CommandContext(ctx, os.Args[0], "--user-manager", "S-1-5-21-1-2-3-1001", "--base-dir", base)
			cmd.Env = append(os.Environ(), "WINUNITD_TEST_DAEMON=1", "WINUNITD_TEST_USER_PIPE="+name)
			out, err := cmd.CombinedOutput()
			if err == nil || cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 2 || !strings.Contains(string(out), "requires an isolated user endpoint") {
				t.Fatalf("unsafe endpoint was not rejected: %v: %s", err, out)
			}
			entries, err := os.ReadDir(base)
			if err != nil || len(entries) != 0 {
				t.Fatalf("rejected daemon performed setup: entries=%v, error=%v", entries, err)
			}
		})
	}
}

type execUserProc struct {
	cmd  *exec.Cmd
	sid  string
	done chan struct{}
}

func (p *execUserProc) PID() int    { return p.cmd.Process.Pid }
func (p *execUserProc) SID() string { return p.sid }
func (p *execUserProc) Alive() bool {
	select {
	case <-p.done:
		return false
	default:
		return p.cmd.Process != nil
	}
}
func (p *execUserProc) Kill() error {
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
	}
	return nil
}
func (p *execUserProc) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return nil
	}
}

func TestWindowsUserManagerOneshotEnableStartAndLogoff(t *testing.T) {
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
	units := filepath.Join(userDir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	outFile := filepath.Join(userDir, "out.txt")
	exe, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	execJSON, err := json.Marshal([]string{exe})
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(""+
		"[Unit]\n"+
		"Description=Hermes Agent\n"+
		"[Service]\n"+
		"Type=oneshot\n"+
		"ExecStart=%s\n"+
		"WorkingDirectory=%s\n"+
		"Environment=\"WINUNITD_USER_ONESHOT=%s\"\n"+
		"[Install]\n"+
		"WantedBy=default.target\n",
		execJSON, userDir, outFile)
	if err := os.WriteFile(filepath.Join(units, "hermes.service"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	sysDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sysDir, "units"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sysDir, "units", "system.service"), []byte(`
[Service]
ExecStart=C:\Tools\system.exe
WorkingDirectory=C:\Tools
`), 0o644); err != nil {
		t.Fatal(err)
	}
	sys, err := manager.New(manager.Config{BaseDir: sysDir, Launch: runtimetest.Launcher()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sys.Close)
	if _, err := sys.Reload(); err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(userDir, "mgr.log")
	logf, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logf.Close()

	h := manager.NewUserHost(manager.UserHostConfig{
		Admission:      manager.UserAdmission{Mode: "unit-files"},
		ProbeUserUnits: func(*runtime.UserToken) (bool, error) { return true, nil },
		Exe:            os.Args[0],
		ExtraArgs:      []string{"--base-dir", userDir},
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

	var out, errb strings.Builder
	code := runCLIUser([]string{"--user", "enable", "hermes"}, &out, &errb, failDial, userDial)
	if code != 0 {
		t.Fatalf("enable exit %d stderr=%s log=\n%s", code, errb.String(), readFile(t, logPath))
	}
	out.Reset()
	errb.Reset()
	code = runCLIUser([]string{"--user", "start", "hermes"}, &out, &errb, failDial, userDial)
	if code != 0 {
		t.Fatalf("start exit %d stdout=%s stderr=%s logs=%s log=\n%s", code, out.String(), errb.String(), userLogs(t, userDial, "hermes"), readFile(t, logPath))
	}

	deadline := time.Now().Add(5 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(outFile)
		if err == nil && strings.Contains(string(b), "USER=") {
			got = string(b)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(got, "USER="+info.Username) {
		t.Fatalf("oneshot output = %q, want USER=%s; log=\n%s", got, info.Username, readFile(t, logPath))
	}
	if !strings.Contains(got, "PROFILE="+info.Profile) {
		t.Fatalf("oneshot missing deterministic USERPROFILE: %q", got)
	}
	if !strings.Contains(got, "LOCAL=") || !strings.Contains(got, "AppData") {
		t.Fatalf("oneshot missing deterministic LOCALAPPDATA: %q", got)
	}

	sysList, err := sys.ListUnits()
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range sysList.Units {
		if u.Name == "hermes.service" {
			t.Fatal("system list-units must not show the user oneshot")
		}
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
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := userDial(ctx); err == nil {
		t.Fatal("user control pipe should be gone after logoff")
	}
}

func failDial(ctx context.Context) (net.Conn, error) {
	_ = ctx
	return nil, fmt.Errorf("system pipe must not be used")
}

func waitUserPipe(t *testing.T, dial func(context.Context) (net.Conn, error)) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		conn, err := dial(ctx)
		cancel()
		if err == nil {
			_ = conn.Close()
			return
		}
		last = err
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("user pipe not ready: %v", last)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return err.Error()
	}
	return string(b)
}

func userLogs(t *testing.T, dial func(context.Context) (net.Conn, error), unit string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := dial(ctx)
	if err != nil {
		return err.Error()
	}
	defer conn.Close()
	got, err := protocol.NewClient(conn).Logs(ctx, protocol.LogsParams{Unit: unit})
	if err != nil {
		return err.Error()
	}
	var b strings.Builder
	for _, e := range got.Entries {
		fmt.Fprintf(&b, "%s %s\n", e.Stream, e.Message)
	}
	return b.String()
}

// runCLIUser is defined in cmd/winctl; this package is cmd/winunitd.
// Dial the user manager with the protocol client instead of winctl for
// enable/start so this test does not import main from winctl.
func runCLIUser(args []string, out, errb *strings.Builder, systemDial, userDial func(context.Context) (net.Conn, error)) int {
	_ = systemDial
	if len(args) < 2 || args[0] != "--user" {
		fmt.Fprintln(errb, "expected --user")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := userDial(ctx)
	if err != nil {
		fmt.Fprintf(errb, "cannot connect: %v\n", err)
		return 1
	}
	defer conn.Close()
	cl := protocol.NewClient(conn)
	cmd := args[1]
	rest := args[2:]
	switch cmd {
	case "enable":
		r, err := cl.Enable(ctx, rest[0])
		if err != nil {
			fmt.Fprintf(errb, "%v\n", err)
			return 1
		}
		fmt.Fprintf(out, "%s: enabled\n", r.Unit)
		return 0
	case "start":
		r, err := cl.Start(ctx, rest[0])
		if err != nil {
			fmt.Fprintf(errb, "%v\n", err)
			return 1
		}
		fmt.Fprintf(out, "%s: %s\n", r.Unit, r.ActiveState)
		return 0
	case "list-units":
		r, err := cl.ListUnits(ctx)
		if err != nil {
			fmt.Fprintf(errb, "%v\n", err)
			return 1
		}
		for _, u := range r.Units {
			fmt.Fprintln(out, u.Name)
		}
		return 0
	default:
		fmt.Fprintf(errb, "unsupported %q\n", cmd)
		return 2
	}
}
