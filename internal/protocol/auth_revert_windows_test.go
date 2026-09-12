//go:build windows

package protocol

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"testing"

	"golang.org/x/sys/windows"
)

// Keep the child's initial thread occupied: runtime.mexit parks m0 instead of
// terminating it. The native termination assertion targets an ordinary worker.
func init() {
	if os.Getenv("WINUNITD_TEST_REVERT_THREAD") == "1" {
		runtime.LockOSThread()
	}
}

func TestPipeRevertFailureRetiresImpersonatingThread(t *testing.T) {
	if os.Getenv("WINUNITD_TEST_REVERT_THREAD") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestPipeRevertFailureRetiresImpersonatingThread$", "-test.timeout=15s")
		cmd.Env = append(os.Environ(), "WINUNITD_TEST_REVERT_THREAD=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("revert thread fixture: %v\n%s", err, output)
		}
		return
	}
	var primary windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &primary); err != nil {
		t.Fatal(err)
	}
	defer primary.Close()
	var token windows.Token
	if err := windows.DuplicateTokenEx(primary, windows.TOKEN_QUERY|windows.TOKEN_IMPERSONATE, nil, windows.SecurityImpersonation, windows.TokenImpersonation, &token); err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	var thread windows.Handle
	cause := errors.New("injected revert failure")
	p, err := peerFromImpersonationWith(0, "", func(windows.Handle) error {
		return windows.SetThreadToken(nil, token)
	}, func(string) (Peer, error) {
		var err error
		thread, err = windows.OpenThread(windows.SYNCHRONIZE, false, windows.GetCurrentThreadId())
		return Peer{Administrator: true}, err
	}, func() error { return cause })
	if thread == 0 {
		t.Fatalf("thread fixture unavailable: %v", err)
	}
	defer windows.CloseHandle(thread)
	if !errors.Is(err, cause) || p != (Peer{}) {
		t.Fatalf("revert failure authorized peer: %+v, %v", p, err)
	}
	var identityErr *impersonatedIdentityError
	if !errors.As(err, &identityErr) {
		t.Fatal("revert failure could enter PID fallback")
	}
	status, waitErr := windows.WaitForSingleObject(thread, 5000)
	if waitErr != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("impersonating thread was not terminated: %v, %v", status, waitErr)
	}
}

func TestPipeTokenFailureRevertsAndStaysTerminal(t *testing.T) {
	cause := errors.New("injected token query failure")
	reverted := false
	p, err := peerFromImpersonationWith(0, "", func(windows.Handle) error { return nil }, func(string) (Peer, error) {
		return Peer{Administrator: true}, cause
	}, func() error { reverted = true; return nil })
	var identityErr *impersonatedIdentityError
	if !reverted || p != (Peer{}) || !errors.Is(err, cause) || !errors.As(err, &identityErr) {
		t.Fatalf("token failure contract: %+v, %v", p, err)
	}
}
