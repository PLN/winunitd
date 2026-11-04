//go:build windows

package runtime

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// waitBound limits every wait under test. waitProcess on an exited process
// must return promptly; a regression to the STILL_ACTIVE spin would never
// return, so the tests fail at this bound instead of hanging the package.
const waitBound = 10 * time.Second

// startWaitTestChild starts a sleeping helper and returns a process handle
// that the test owns. The helper is terminated and reaped at cleanup.
func startWaitTestChild(t *testing.T) windows.Handle {
	t.Helper()
	cmd := exec.Command(testAbs(t), winunitdHelperArgPrefix+"sleep")
	cmd.Env = helperEnv("WINUNITD_JOB_HELPER=sleep")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	h, err := windows.OpenProcess(
		windows.PROCESS_TERMINATE|windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = windows.TerminateProcess(h, 1)
		_ = windows.CloseHandle(h)
		_ = cmd.Wait()
	})
	return h
}

func duplicateWaitHandle(t *testing.T, h windows.Handle) windows.Handle {
	t.Helper()
	var dup windows.Handle
	if err := windows.DuplicateHandle(
		windows.CurrentProcess(), h,
		windows.CurrentProcess(), &dup,
		0, false, windows.DUPLICATE_SAME_ACCESS,
	); err != nil {
		t.Fatal(err)
	}
	return dup
}

// boundedCall runs call and fails the test if it does not return within
// waitBound. A failed bound leaks the stuck goroutine; the test has failed.
func boundedCall(t *testing.T, call func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- call() }()
	select {
	case err := <-done:
		return err
	case <-time.After(waitBound):
		t.Fatalf("call did not return within %s", waitBound)
		return nil
	}
}

// boundedWaitProcess runs waitProcess on a duplicate of h within waitBound.
func boundedWaitProcess(t *testing.T, h windows.Handle, ctx context.Context, record func(uint32)) error {
	t.Helper()
	dup := duplicateWaitHandle(t, h)
	return boundedCall(t, func() error { return waitProcess(ctx, dup, record) })
}

func TestWaitProcessTreatsExitCode259AsExit(t *testing.T) {
	h := startWaitTestChild(t)
	workload := &winProc{process: h}
	if !workload.Alive() {
		t.Fatal("live workload reported exited")
	}

	// Live process: the handle is not signalled and a cancelled wait
	// returns the context error without recording an exit.
	if exited, err := processHandleExited(h); err != nil || exited {
		t.Fatalf("live process: exited=%v err=%v", exited, err)
	}
	recorded := false
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	err := boundedWaitProcess(t, h, ctx, func(uint32) { recorded = true })
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait on live process = %v, want context.DeadlineExceeded", err)
	}
	if recorded {
		t.Fatal("cancelled wait recorded an exit code")
	}

	// Exit with STILL_ACTIVE as the final code.
	if err := windows.TerminateProcess(h, stillActiveExit); err != nil {
		t.Fatal(err)
	}
	var codes []uint32
	ctx, cancel = context.WithTimeout(context.Background(), waitBound)
	err = boundedWaitProcess(t, h, ctx, func(code uint32) { codes = append(codes, code) })
	cancel()
	var exit *ExitStatus
	if !errors.As(err, &exit) || exit.Code != stillActiveExit {
		t.Fatalf("Wait after exit 259 = %v, want ExitStatus{Code: %d}", err, stillActiveExit)
	}
	if len(codes) != 1 || codes[0] != stillActiveExit {
		t.Fatalf("recorded exit codes = %v, want [%d]", codes, stillActiveExit)
	}
	if exited, err := processHandleExited(h); err != nil || !exited {
		t.Fatalf("exited process: exited=%v err=%v", exited, err)
	}
	// No winProc.Wait has recorded this exit: Alive must inspect the handle.
	if workload.Alive() {
		t.Fatal("Alive reports a workload that exited with code 259")
	}

	// A cancelled caller on an exited-259 process must also return: the
	// signalled process handle must not keep the cancel event from winning.
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	err = boundedWaitProcess(t, h, ctx, nil)
	if !errors.As(err, &exit) && !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Wait after exit 259 = %v", err)
	}
}
