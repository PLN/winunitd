//go:build windows

package runtime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestBrokerJobBoundary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, testAbs(t), "-test.run=^TestBrokerJobHelper$")
	cmd.Env = append(helperEnv(), "WINUNITD_BROKER_JOB_TEST=owner")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("isolated broker: %v\n%s", err, out)
	}
}

func TestBrokerCrashKillsIndependentUserJob(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ready := filepath.Join(t.TempDir(), "child.pid")
	cmd := exec.CommandContext(ctx, testAbs(t), "-test.run=^TestBrokerJobHelper$")
	cmd.Env = append(helperEnv(), "WINUNITD_BROKER_JOB_TEST=crash", "WINUNITD_BROKER_READY="+ready)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	var pid int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(ready); err == nil {
			pid, _ = strconv.Atoi(string(data))
			if pid > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid <= 0 {
		t.Fatal("broker did not publish an owned child")
	}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if state, err := windows.WaitForSingleObject(h, 5000); err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("broker crash left independent child alive: %v %v", state, err)
	}
}

func TestBrokerJobHelper(t *testing.T) {
	mode := os.Getenv("WINUNITD_BROKER_JOB_TEST")
	if mode == "" {
		return
	}
	if mode == "confined" {
		cmd := exec.Command(testAbs(t), winunitdHelperArgPrefix+"sleep")
		cmd.Env = helperEnv("WINUNITD_JOB_HELPER=sleep")
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_BREAKAWAY_FROM_JOB}
		if err := cmd.Start(); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			if err == nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
			t.Fatalf("workload breakaway = %v, want access denied", err)
		}
		return
	}
	broker, err := OpenBrokerJob()
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	var session uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session); err != nil {
		t.Fatal(err)
	}
	if _, _, err := userManagerJobBoundary(broker, session+1); err == nil {
		t.Fatal("unassigned broker accepted")
	}
	if err := broker.AssignSelf(); err != nil {
		t.Fatal(err)
	}
	outer, flags, err := userManagerJobBoundary(broker, session+1)
	if err != nil || outer != nil || flags != windows.CREATE_BREAKAWAY_FROM_JOB {
		t.Fatalf("cross-session boundary: outer=%v flags=%x err=%v", outer, flags, err)
	}
	if same, flags, err := userManagerJobBoundary(broker, session); err != nil || same != broker || flags != 0 {
		t.Fatal("same-session boundary changed")
	}
	job, err := OpenDaemonJob()
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	if _, _, err := userManagerJobBoundary(job, session+1); err == nil {
		t.Fatal("ordinary job allowed cross-session breakaway")
	}
	token := testUserToken(t)
	tok, _ := nativeToken(token)
	// Exercise the exact atomic launch mechanism without requiring a privileged
	// token/session change on a development host. Genuine session placement is
	// separately qualified in the disposable SYSTEM guest.
	proc, err := createUserManagerWithBoundary(tok, UserManagerSpec{
		SID: token.Info.SID, Token: token, Exe: testAbs(t),
		Env:       helperEnv("WINUNITD_JOB_HELPER=sleep"),
		ExtraArgs: []string{winunitdHelperArgPrefix + "sleep"},
	}, job, outer, flags)
	if proc != nil {
		defer proc.Kill()
	}
	if err != nil {
		t.Fatal(err)
	}
	if member, err := isProcessInJob(proc.process, broker.handle); err != nil || member {
		t.Fatalf("child retained broker membership: %v %v", member, err)
	}
	if member, err := isProcessInJob(proc.process, job.handle); err != nil || !member {
		t.Fatalf("child missing dedicated membership: %v %v", member, err)
	}
	if mode == "crash" {
		if err := os.WriteFile(os.Getenv("WINUNITD_BROKER_READY"), []byte(strconv.Itoa(proc.PID())), 0600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Minute) // parent terminates the broker without cleanup
		t.Fatal("parent did not terminate broker")
	}
	if err := proc.Kill(); err != nil {
		t.Fatal(err)
	}
	// A manager in a no-breakaway child job cannot use the broker's relaxed
	// outer limit to escape. This also models nested SYSTEM unit confinement.
	child, err := StartUserManager(UserManagerSpec{
		SID: token.Info.SID, Token: token, Exe: testAbs(t), Daemon: broker,
		Env:     append(helperEnv(), "WINUNITD_BROKER_JOB_TEST=confined"),
		cmdArgv: []string{testAbs(t), "-test.run=^TestBrokerJobHelper$"},
	})
	if child != nil {
		defer child.Kill()
	}
	if err != nil {
		t.Fatal(err)
	}
	p := child.(*userMgrProc)
	if state, err := windows.WaitForSingleObject(p.process, 10000); err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("confined child completion: %v %v", state, err)
	}
	var code uint32
	if err := windows.GetExitCodeProcess(p.process, &code); err != nil || code != 0 {
		t.Fatalf("confined child failed: exit=%d err=%v", code, err)
	}
}
