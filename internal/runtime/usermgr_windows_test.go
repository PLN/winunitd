//go:build windows

package runtime

import (
	"os"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestStartUserManagerCreateProcessAsUser(t *testing.T) {
	var tok windows.Token
	if err := windows.OpenProcessToken(
		windows.CurrentProcess(),
		windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY|windows.TOKEN_ASSIGN_PRIMARY,
		&tok,
	); err != nil {
		t.Fatal(err)
	}
	defer tok.Close()

	var primary windows.Token
	if err := windows.DuplicateTokenEx(
		tok,
		windows.TOKEN_ASSIGN_PRIMARY|windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY|windows.TOKEN_ADJUST_DEFAULT|windows.TOKEN_ADJUST_SESSIONID,
		nil,
		windows.SecurityImpersonation,
		windows.TokenPrimary,
		&primary,
	); err != nil {
		t.Fatal(err)
	}
	info, err := userInfoFromToken(primary)
	if err != nil {
		primary.Close()
		t.Fatal(err)
	}
	userTok := &UserToken{Info: info, native: winToken(primary)}
	defer userTok.Close()

	env := MergeDeterministicUserEnv(helperEnv("WINUNITD_JOB_HELPER=sleep"), info)
	proc, err := StartUserManager(UserManagerSpec{
		SID:   info.SID,
		Token: userTok,
		Exe:   testAbs(t),
		Env:   env,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Kill()
	if !proc.Alive() {
		t.Fatal("user manager process died immediately")
	}
	if proc.PID() <= 0 {
		t.Fatalf("pid = %d", proc.PID())
	}
	if proc.SID() != info.SID {
		t.Fatalf("SID = %q, want %q", proc.SID(), info.SID)
	}
	if err := proc.Kill(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !proc.Alive() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("Kill did not tear down the user manager")
}

func TestStartUserManagerFailsClosedWithoutNativeToken(t *testing.T) {
	info, err := CurrentUserInfo()
	if err != nil {
		t.Fatal(err)
	}
	_, err = StartUserManager(UserManagerSpec{
		SID:   info.SID,
		Token: &UserToken{Info: info},
		Exe:   os.Args[0],
	})
	if err == nil {
		t.Fatal("missing WTS token handle must fail closed")
	}
}
