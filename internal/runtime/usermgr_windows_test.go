//go:build windows

package runtime

import (
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func testUserToken(t *testing.T) *UserToken {
	t.Helper()
	var tok windows.Token
	if err := windows.OpenProcessToken(
		windows.CurrentProcess(),
		windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY|windows.TOKEN_ASSIGN_PRIMARY,
		&tok,
	); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tok.Close() })

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
		_ = primary.Close()
		t.Fatal(err)
	}
	userTok := &UserToken{Info: info, native: winToken(primary)}
	t.Cleanup(func() { _ = userTok.Close() })
	return userTok
}

func TestStartUserManagerCreateProcessAsUser(t *testing.T) {
	userTok := testUserToken(t)
	env := MergeDeterministicUserEnv(helperEnv("WINUNITD_JOB_HELPER=sleep"), userTok.Info)
	proc, err := StartUserManager(UserManagerSpec{
		SID:   userTok.Info.SID,
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
	if proc.SID() != userTok.Info.SID {
		t.Fatalf("SID = %q, want %q", proc.SID(), userTok.Info.SID)
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

func TestUserManagerStdioWriteAccess(t *testing.T) {
	userTok := testUserToken(t)
	pipeName := `\\.\pipe\winunitd-um-stdio-` + strconv.Itoa(os.Getpid()) + `-` + strconv.FormatInt(time.Now().UnixNano(), 10)
	ln, err := winio.ListenPipe(pipeName, &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;WD)",
		InputBufferSize:    4096,
		OutputBufferSize:   4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			ch <- result{err: err}
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		b, err := io.ReadAll(c)
		if err != nil && len(b) == 0 {
			ch <- result{err: err}
			return
		}
		ch <- result{line: strings.TrimSpace(string(b))}
	}()

	env := MergeDeterministicUserEnv(helperEnv(
		"WINUNITD_JOB_HELPER=stdio-write",
		"WINUNITD_STDIO_REPORT_PIPE="+pipeName,
	), userTok.Info)
	proc, err := StartUserManager(UserManagerSpec{
		SID:       userTok.Info.SID,
		Token:     userTok,
		Exe:       testAbs(t),
		Env:       env,
		ExtraArgs: []string{winunitdHelperArgPrefix + "stdio-write"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Kill()

	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.line != "stdout=ok stderr=ok" {
			t.Fatalf("stdio report = %q, want stdout=ok stderr=ok", r.line)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for stdio WriteFile report")
	}
}

func TestUserManagerCreateProcessDoesNotInheritListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	userTok := testUserToken(t)
	env := MergeDeterministicUserEnv(helperEnv("WINUNITD_JOB_HELPER=sleep"), userTok.Info)
	proc, err := StartUserManager(UserManagerSpec{
		SID:   userTok.Info.SID,
		Token: userTok,
		Exe:   testAbs(t),
		Env:   env,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Kill()
	time.Sleep(400 * time.Millisecond)
	if !proc.Alive() {
		t.Fatal("user manager died while parent held a loopback listener")
	}
}
