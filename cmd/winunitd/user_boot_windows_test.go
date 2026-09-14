//go:build windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/manager"
	"github.com/PLN/winunitd/internal/protocol"
	"golang.org/x/sys/windows"
)

const pendingUserBootUnit = "pending.service"

func prepareUserNotifyBoot(t *testing.T) (string, string, string, string) {
	t.Helper()
	sid, err := protocol.CurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	marker := filepath.Join(base, "pending-pid")
	exe, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	command, err := json.Marshal([]string{exe})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "units"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("[Service]\nType=notify\nExecStart=%s\nEnvironment=\"WINUNITD_USER_PENDING_NOTIFY=%s\"\nTimeoutStartSec=30s\nTimeoutStopSec=1s\n", command, marker)
	if err := os.WriteFile(filepath.Join(base, "units", pendingUserBootUnit), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// Enable offline so the real user entry point encounters a pending notify
	// workload during Boot, before any client starts or stops a unit.
	m, err := manager.New(manager.Config{BaseDir: base, UserScope: true})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Enable(pendingUserBootUnit); err != nil {
		t.Fatal(err)
	}

	return sid, base, exe, marker
}

func TestWindowsUserControlRemainsAvailableDuringNotifyBoot(t *testing.T) {
	sid, base, exe, marker := prepareUserNotifyBoot(t)
	const name = pendingUserBootUnit

	log, err := os.Create(filepath.Join(base, "daemon.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	pipe := newTestUserPipe()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "--user-manager", sid, "--base-dir", base)
	cmd.Env = append(os.Environ(), "WINUNITD_TEST_DAEMON=1", "WINUNITD_TEST_USER_PIPE="+pipe)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	deadline := time.Now().Add(5 * time.Second)
	var childPID int
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(marker); err == nil {
			childPID, _ = strconv.Atoi(string(data))
			if childPID > 0 {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if childPID <= 0 {
		t.Fatal("enabled notify workload did not enter boot")
	}
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(childPID))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(process)
	dialCtx, dialCancel := context.WithTimeout(ctx, 2*time.Second)
	defer dialCancel()
	conn, err := protocol.DialPipe(dialCtx, pipe)
	if err != nil {
		t.Fatalf("pending notify boot hid user control: %v", err)
	}
	defer conn.Close()
	client := protocol.NewClient(conn)
	status, err := client.Status(ctx, name)
	if err != nil || status.Unit.ActiveState != core.Activating.String() || status.Unit.MainPID != childPID {
		t.Fatalf("pending notify status unavailable: %+v %v", status, err)
	}
	if _, err := client.Stop(ctx, name); err != nil {
		t.Fatalf("could not stop the pending boot workload: %v", err)
	}
	if state, err := windows.WaitForSingleObject(process, 5000); err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("stop retained the native workload: state=%d error=%v", state, err)
	}
	status, err = client.Status(ctx, name)
	if err != nil || status.Unit.MainPID != 0 || status.Unit.ActiveState != core.Inactive.String() || status.Unit.TerminationUncertain {
		t.Fatalf("pending boot cleanup did not settle: %+v %v", status, err)
	}
}

func TestWindowsUserControlBindFailureDoesNotLaunchEnabledUnits(t *testing.T) {
	sid, base, exe, marker := prepareUserNotifyBoot(t)
	pipe := newTestUserPipe()
	sddl, err := protocol.UserPipeSDDL(sid)
	if err != nil {
		t.Fatal(err)
	}
	occupied, err := protocol.ListenPipeSDDL(pipe, sddl)
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "--user-manager", sid, "--base-dir", base)
	cmd.Env = append(os.Environ(), "WINUNITD_TEST_DAEMON=1", "WINUNITD_TEST_USER_PIPE="+pipe)
	out, err := cmd.CombinedOutput()
	if err == nil || ctx.Err() != nil || !strings.Contains(string(out), "listen:") {
		t.Fatalf("user manager did not reject the occupied endpoint before boot: %v: %s", err, out)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("unreachable user manager launched an enabled workload: %v", err)
	}
}
