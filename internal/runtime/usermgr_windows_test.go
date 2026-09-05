//go:build windows

package runtime

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	goruntime "runtime"
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
		SID:       userTok.Info.SID,
		Token:     userTok,
		Exe:       testAbs(t),
		Env:       env,
		ExtraArgs: []string{winunitdHelperArgPrefix + "sleep"},
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

	// Same child as TestCreateProcessWithLoopbackListenerKeepsHelperAlive.
	// A Go test-binary helper can die on an inherited overlapped socket even
	// when ping (the unit-path regression) stays alive; ExtraArgs sleep was
	// not enough to keep that helper running under CreateProcessAsUser.
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	ping := filepath.Join(root, "System32", "ping.exe")
	if _, err := os.Stat(ping); err != nil {
		t.Fatalf("ping.exe: %v", err)
	}

	userTok := testUserToken(t)
	proc, err := StartUserManager(UserManagerSpec{
		SID:     userTok.Info.SID,
		Token:   userTok,
		Exe:     ping,
		Env:     helperEnv(),
		cmdArgv: []string{ping, "-t", "127.0.0.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Kill()
	if !proc.Alive() {
		t.Fatalf("user manager died immediately (exit=%d)", userManagerExitCode(proc))
	}
	time.Sleep(400 * time.Millisecond)
	if !proc.Alive() {
		t.Fatalf("user manager died while parent held a loopback listener (exit=%d)", userManagerExitCode(proc))
	}
}

func userManagerExitCode(proc UserManagerProc) uint32 {
	p, ok := proc.(*userMgrProc)
	if !ok || p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.process == 0 {
		return 0
	}
	var code uint32
	_ = windows.GetExitCodeProcess(p.process, &code)
	return code
}

func TestUserManagerWaitCancelDoesNotPoll(t *testing.T) {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	ping := filepath.Join(root, "System32", "ping.exe")
	if _, err := os.Stat(ping); err != nil {
		t.Fatalf("ping.exe: %v", err)
	}

	userTok := testUserToken(t)
	proc, err := StartUserManager(UserManagerSpec{
		SID:     userTok.Info.SID,
		Token:   userTok,
		Exe:     ping,
		Env:     helperEnv(),
		cmdArgv: []string{ping, "-t", "127.0.0.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Kill()
	if !proc.Alive() {
		t.Fatalf("user manager died immediately (exit=%d)", userManagerExitCode(proc))
	}

	goruntime.GC()
	baseline := goruntime.NumGoroutine()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	err = proc.Wait(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait = %v, want context.DeadlineExceeded", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	var n int
	for time.Now().Before(deadline) {
		n = goruntime.NumGoroutine()
		if n <= baseline+1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n > baseline+2 {
		t.Fatalf("goroutines after cancel: %d (baseline %d)", n, baseline)
	}
	if !proc.Alive() {
		t.Fatal("helper exited during cancelled Wait")
	}
}

func TestUserManagerKillFailureRetainsHandles(t *testing.T) {
	for _, fault := range []string{"terminate", "wait"} {
		t.Run(fault, func(t *testing.T) {
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
			job, process := p.job, p.process
			t.Cleanup(func() { p.job, p.process = job, process; _ = p.Kill() })
			if fault == "terminate" {
				p.job = &DaemonJob{handle: windows.InvalidHandle}
			} else {
				p.process = 0
			}
			if err := p.Kill(); err == nil {
				t.Fatal("failed user-manager cleanup reported success")
			}
			p.mu.Lock()
			closed := p.closed
			p.mu.Unlock()
			if closed || job.Closed() {
				t.Fatal("failed user-manager cleanup discarded handles")
			}
			p.job, p.process = job, process
			if err := p.Kill(); err != nil {
				t.Fatal("cleanup retry failed", err)
			}
			if p.Alive() || !job.Closed() {
				t.Fatal("successful cleanup retained live process or job handle")
			}
		})
	}
}

func TestUserManagerKillConfirmsDescendantExit(t *testing.T) {
	tok := testUserToken(t)
	proc, err := StartUserManager(UserManagerSpec{
		SID: tok.Info.SID, Token: tok, Exe: testAbs(t),
		Env:       helperEnv("WINUNITD_JOB_HELPER=spawn"),
		ExtraArgs: []string{winunitdHelperArgPrefix + "spawn"},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := proc.(*userMgrProc)
	defer p.Kill()
	var child windows.Handle
	deadline := time.Now().Add(5 * time.Second)
	for child == 0 && time.Now().Before(deadline) {
		p.job.mu.Lock()
		ids, err := queryJobPIDs(p.job.handle)
		p.job.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		for _, pid := range ids {
			if pid != p.PID() {
				child, err = windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
				if err != nil {
					t.Fatal(err)
				}
				break
			}
		}
		if child == 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if child == 0 {
		t.Fatal("user-manager helper did not create a descendant")
	}
	defer windows.CloseHandle(child)
	if err := p.Kill(); err != nil {
		t.Fatal(err)
	}
	state, err := windows.WaitForSingleObject(child, 0)
	if err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("descendant still running after Kill: wait=%d err=%v", state, err)
	}
}
