//go:build windows

package runtime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/unit"
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

const stillActive = 259

func processAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}

func terminatePID(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.TerminateProcess(h, 1)
}

func startSleepHelper(t *testing.T, inherit []windows.Handle) *exec.Cmd {
	t.Helper()
	start := func(breakaway bool) (*exec.Cmd, error) {
		cmd := exec.Command(os.Args[0], winunitdHelperArgPrefix+"sleep")
		cmd.Env = helperEnv("WINUNITD_JOB_HELPER=sleep")
		flags := uint32(windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW)
		if breakaway {
			flags |= windows.CREATE_BREAKAWAY_FROM_JOB
		}
		sys := &windows.SysProcAttr{
			HideWindow:    true,
			CreationFlags: flags,
		}
		if len(inherit) > 0 {
			hs := make([]syscall.Handle, len(inherit))
			for i, h := range inherit {
				hs[i] = syscall.Handle(h)
			}
			sys.AdditionalInheritedHandles = hs
		}
		cmd.SysProcAttr = sys
		return cmd, cmd.Start()
	}

	cmd, err := start(true)
	if err != nil {
		cmd, err = start(false)
		if err != nil {
			t.Fatal(err)
		}
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
	sleeper := startSleepHelper(t, nil)
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
	if !job.Closed() {
		t.Fatal("Close must mark the daemon job closed")
	}
	waitDone(t, sleeper, 5*time.Second)
}

func TestKillDaemonTearsDownJob(t *testing.T) {
	// Create the job in this process. Killing the CreateJobObject process
	// ACCESS_DENIED on GitHub windows-latest (outer runner job + nested job).
	// Duplicate an inheritable handle into a sleep helper, drop parent
	// handles, then TerminateProcess the helper. That is process-death
	// releasing the last job handle — the same KILL_ON_JOB_CLOSE path as
	// killing winunitd.exe (DESIGN.md §66).
	job, err := OpenDaemonJob()
	if err != nil {
		t.Fatal(err)
	}

	sleeper := startSleepHelper(t, nil)
	if err := job.AssignPID(sleeper.Process.Pid); err != nil {
		t.Fatal(err)
	}

	inherited, err := job.inheritDup()
	if err != nil {
		t.Fatal(err)
	}
	holder := startSleepHelper(t, []windows.Handle{inherited})
	if err := windows.CloseHandle(inherited); err != nil {
		t.Fatal(err)
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}

	if !processAlive(sleeper.Process.Pid) {
		t.Fatal("sleeper died before holder was killed; inherited handle was not keeping the job open")
	}

	if err := terminatePID(holder.Process.Pid); err != nil {
		if processAlive(holder.Process.Pid) {
			t.Fatalf("kill holder pid %d: %v", holder.Process.Pid, err)
		}
	}
	_ = holder.Wait()
	holder.Process = nil
	waitDone(t, sleeper, 5*time.Second)
}

func TestAssignDaemonPIDVerifiesExistingMembership(t *testing.T) {
	sleeper := startSleepHelper(t, nil)
	job, err := OpenDaemonJob()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = job.Close() })
	if err := job.AssignPID(sleeper.Process.Pid); err != nil {
		t.Fatal(err)
	}
	if err := assignDaemonPID(job, sleeper.Process.Pid); err != nil {
		t.Fatalf("verified membership must succeed: %v", err)
	}
}

func TestAssignDaemonPIDDoesNotIgnoreOpenProcessDenied(t *testing.T) {
	job, err := OpenDaemonJob()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = job.Close() })
	// Pid 4 is the System process; OpenProcess typically ACCESS_DENIED.
	err = assignDaemonPID(job, 4)
	if err == nil {
		t.Fatal("System process assignment unexpectedly succeeded")
	}
}

func TestStartFailsWhenDaemonJobClosed(t *testing.T) {
	job, err := OpenDaemonJob()
	if err != nil {
		t.Fatal(err)
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	p, err := NewLauncher(job).Start(context.Background(), StartSpec{
		Unit: "closed-daemon.service",
		Type: unit.TypeSimple,
		Argv: []string{testAbs(t), winunitdHelperArgPrefix + "sleep"},
		Dir:  t.TempDir(),
		Env:  helperEnv("WINUNITD_JOB_HELPER=sleep"),
	})
	if p != nil {
		_ = p.Stop(time.Second)
	}
	if err == nil {
		t.Fatal("Start must fail when daemon job assignment fails")
	}
}

func queryOnlyDaemonJob(t *testing.T) *DaemonJob {
	t.Helper()
	job, err := OpenDaemonJob()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = job.Close() })
	var h windows.Handle
	if err := windows.DuplicateHandle(windows.CurrentProcess(), job.handle,
		windows.CurrentProcess(), &h, 0x0004 /* JOB_OBJECT_QUERY */, false, 0); err != nil {
		t.Fatal(err)
	}
	limited := &DaemonJob{handle: h}
	t.Cleanup(func() { _ = limited.Close() })
	return limited
}

func TestAssignDaemonRejectsUnverifiedAccessDenied(t *testing.T) {
	job := queryOnlyDaemonJob(t)
	sleeper := startSleepHelper(t, nil)
	err := assignDaemonPID(job, sleeper.Process.Pid)
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("assignment without assign rights: %v", err)
	}
	member, err := isProcessInJob(windows.CurrentProcess(), job.handle)
	if err != nil || member {
		t.Fatalf("unexpected test-parent membership: member=%v err=%v", member, err)
	}
}

func TestLaunchRejectsUnownedDaemonJob(t *testing.T) {
	t.Run("unit", func(t *testing.T) {
		job := queryOnlyDaemonJob(t)
		proc, err := NewLauncher(job).Start(context.Background(), StartSpec{
			Unit: "unowned.service", Type: unit.TypeSimple,
			Argv: []string{testAbs(t), winunitdHelperArgPrefix + "sleep"}, Env: helperEnv("WINUNITD_JOB_HELPER=sleep"),
		})
		if proc != nil {
			t.Cleanup(func() { _ = proc.Stop(time.Second) })
		}
		if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			t.Fatalf("unowned launch result: %v", err)
		}
		if proc != nil {
			t.Fatal("failed unit launch did not finish cleanup")
		}
	})
	t.Run("user", func(t *testing.T) {
		job := queryOnlyDaemonJob(t)
		tok := testUserToken(t)
		proc, err := StartUserManager(UserManagerSpec{
			SID: tok.Info.SID, Token: tok, Exe: testAbs(t), Daemon: job,
			ExtraArgs: []string{winunitdHelperArgPrefix + "sleep"}, Env: helperEnv("WINUNITD_JOB_HELPER=sleep"),
		})
		if proc != nil {
			t.Cleanup(func() { _ = proc.Kill() })
		}
		if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			t.Fatalf("unowned launch result: %v", err)
		}
		if proc != nil {
			t.Fatal("failed user launch did not finish cleanup")
		}
	})
}
