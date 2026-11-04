//go:build windows

package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// A user manager that exits with code 259 (STILL_ACTIVE) is exited: Alive
// reports false, Wait returns the exit status, and Kill, the logoff cleanup
// path, completes instead of spinning in the waiter.
func TestUserManagerExitCode259IsExited(t *testing.T) {
	tok := testUserToken(t)
	proc, err := StartUserManager(UserManagerSpec{
		SID: tok.Info.SID, Token: tok, Exe: testAbs(t),
		Env:       helperEnv("WINUNITD_JOB_HELPER=sleep"),
		ExtraArgs: []string{winunitdHelperArgPrefix + "sleep"},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := proc.(*userMgrProc)
	killed := false
	t.Cleanup(func() {
		if !killed {
			// Cleanup must also be bounded: a regression may leave Wait
			// spinning or a previous Kill holding killMu.
			_ = boundedCall(t, p.Kill)
		}
	})

	// Live state, including across a cancelled wait.
	if !p.Alive() {
		t.Fatalf("user manager died immediately (exit=%d)", userManagerExitCode(p))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	err = boundedCall(t, func() error { return p.Wait(ctx) })
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait on live user manager = %v, want context.DeadlineExceeded", err)
	}
	if !p.Alive() {
		t.Fatal("user manager exited during cancelled Wait")
	}

	p.mu.Lock()
	process := p.process
	p.mu.Unlock()
	if err := windows.TerminateProcess(process, stillActiveExit); err != nil {
		t.Fatal(err)
	}

	ctx, cancel = context.WithTimeout(context.Background(), waitBound)
	err = boundedCall(t, func() error { return p.Wait(ctx) })
	cancel()
	var exit *ExitStatus
	if !errors.As(err, &exit) || exit.Code != stillActiveExit {
		t.Fatalf("Wait after exit 259 = %v, want ExitStatus{Code: %d}", err, stillActiveExit)
	}
	if code := userManagerExitCode(p); code != stillActiveExit {
		t.Fatalf("exit code = %d, want %d", code, stillActiveExit)
	}
	if p.Alive() {
		t.Fatal("Alive reports a user manager that exited with code 259")
	}

	if err := boundedCall(t, p.Kill); err != nil {
		t.Fatalf("Kill after exit 259: %v", err)
	}
	killed = true
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if !closed || !p.job.Closed() {
		t.Fatal("Kill after exit 259 retained handles")
	}
}
