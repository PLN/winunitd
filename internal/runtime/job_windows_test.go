//go:build windows

package runtime

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func helperEnv(extra ...string) []string {
	out := make([]string, 0, len(os.Environ())+len(extra))
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "WINUNITD_JOB_") {
			continue
		}
		out = append(out, e)
	}
	return append(out, extra...)
}

func TestMain(m *testing.M) {
	switch os.Getenv("WINUNITD_JOB_HELPER") {
	case "sleep":
		select {}
	case "holder":
		os.Exit(runHolder())
	}
	os.Exit(m.Run())
}

func runHolder() int {
	pid, err := strconv.Atoi(os.Getenv("WINUNITD_JOB_CHILD_PID"))
	if err != nil || pid <= 0 {
		return 2
	}
	job, err := OpenDaemonJob()
	if err != nil {
		return 3
	}
	if err := job.AssignPID(pid); err != nil {
		return 4
	}
	if err := os.WriteFile(os.Getenv("WINUNITD_JOB_READYFILE"), []byte("ok\n"), 0o644); err != nil {
		return 5
	}
	// Stay alive until the parent drops stdin or the process is terminated.
	// The job handle is not Closed; process death releases it and
	// KILL_ON_JOB_CLOSE tears down assigned children (DESIGN.md §66).
	_, _ = io.Copy(io.Discard, os.Stdin)
	return 0
}

func startSleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = helperEnv("WINUNITD_JOB_HELPER=sleep")
	cmd.SysProcAttr = &windows.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = terminatePID(cmd.Process.Pid)
			_ = cmd.Wait()
		}
	})
	return cmd
}

func waitDone(t *testing.T, cmd *exec.Cmd, timeout time.Duration) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		cmd.Process = nil
	case <-time.After(timeout):
		t.Fatalf("process pid %d still running after %s", cmd.Process.Pid, timeout)
	}
}

func terminatePID(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.TerminateProcess(h, 1)
}

func TestDaemonJobKillOnCloseFlag(t *testing.T) {
	job, err := OpenDaemonJob()
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	ok, err := job.killOnCloseEnabled()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE not set")
	}
}

func TestCloseJobKillsAssignedProcess(t *testing.T) {
	sleeper := startSleeper(t)
	job, err := OpenDaemonJob()
	if err != nil {
		t.Fatal(err)
	}
	if err := job.AssignPID(sleeper.Process.Pid); err != nil {
		t.Fatal(err)
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	waitDone(t, sleeper, 5*time.Second)
}

func TestKillDaemonTearsDownJob(t *testing.T) {
	sleeper := startSleeper(t)
	ready := filepath.Join(t.TempDir(), "ready")
	holder := exec.Command(os.Args[0])
	holder.Env = helperEnv(
		"WINUNITD_JOB_HELPER=holder",
		"WINUNITD_JOB_CHILD_PID="+strconv.Itoa(sleeper.Process.Pid),
		"WINUNITD_JOB_READYFILE="+ready,
	)
	holder.SysProcAttr = &windows.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
	}
	stdin, err := holder.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = terminatePID(holder.Process.Pid)
			_ = stdin.Close()
			_ = holder.Wait()
			t.Fatal("holder did not become ready")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Drop the daemon process so its job handle is released. TerminateProcess
	// is the kill path; some CI job-object setups deny it on the test binary,
	// in which case closing stdin makes the process exit the same way a crash
	// would for KILL_ON_JOB_CLOSE.
	if err := terminatePID(holder.Process.Pid); err != nil {
		_ = stdin.Close()
	}
	_ = holder.Wait()
	waitDone(t, sleeper, 5*time.Second)
}
