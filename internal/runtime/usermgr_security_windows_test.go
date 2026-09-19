//go:build windows

package runtime_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
	"golang.org/x/sys/windows"
)

func securityListener(t *testing.T, sddl string) (net.Listener, string) {
	t.Helper()
	name := `\\.\pipe\winunitd-security-` + rand.Text()
	ln, err := protocol.ListenPipeSDDL(name, sddl)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln, name
}

func securityDeniedEndpoint(t *testing.T, sddl string) string {
	t.Helper()
	ln, name := securityListener(t, sddl)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.SetDeadline(time.Now().Add(3 * time.Second))
			var ping [1]byte
			if _, err := io.ReadFull(c, ping[:]); err == nil {
				_, _ = c.Write(ping[:])
			}
			c.Close()
		}
	}()
	t.Cleanup(func() { ln.Close(); <-done })
	securityEndpointHealthy(t, name)
	return name
}

func securityEndpointHealthy(t *testing.T, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := protocol.DialPipe(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte{42}); err != nil {
		t.Fatal(err)
	}
	var pong [1]byte
	if _, err := io.ReadFull(c, pong[:]); err != nil || pong[0] != 42 {
		t.Fatal("protected endpoint failed health echo", err)
	}
}

type securityResult struct {
	report runtime.NativeSecurityReport
	peer   protocol.Peer
	err    error
}

func securityReports(ln net.Listener, sid string) <-chan securityResult {
	ch := make(chan securityResult, 1)
	go func() {
		var result securityResult
		c, err := ln.Accept()
		if err != nil {
			result.err = err
			ch <- result
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		result.err = json.NewDecoder(io.LimitReader(c, 4096)).Decode(&result.report)
		if result.err == nil {
			result.peer, result.err = protocol.UserAuthorizer(sid)(c)
		}
		if result.err == nil {
			_, result.err = c.Write([]byte{1})
		}
		ch <- result
	}()
	return ch
}

func awaitSecurityReport(t *testing.T, reports <-chan securityResult) securityResult {
	t.Helper()
	select {
	case result := <-reports:
		if result.err != nil {
			t.Fatal(result.err)
		}
		return result
	case <-time.After(20 * time.Second):
		t.Fatal("child security report timed out")
		return securityResult{}
	}
}

func securitySentinels(t *testing.T) ([]windows.Handle, []runtime.NativeFileIdentity, string) {
	t.Helper()
	var handles []windows.Handle
	var ids []runtime.NativeFileIdentity
	var args []string
	for range 2 {
		f, err := os.CreateTemp(t.TempDir(), "sentinel-*")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Close() })
		h := windows.Handle(f.Fd())
		if err := windows.SetHandleInformation(h, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
			t.Fatal(err)
		}
		id := runtime.NativeHandleFileIdentity(h)
		if id == (runtime.NativeFileIdentity{}) {
			t.Fatal("sentinel has no file identity")
		}
		handles, ids, args = append(handles, h), append(ids, id), append(args, strconv.FormatUint(uint64(h), 10))
	}
	return handles, ids, "--sentinel-handles=" + strings.Join(args, ",")
}

// This positive control explicitly inherits only the first of two inheritable
// files. It proves both detection and discrimination in the child handle table.
func TestNativeSecurityProbeDetectsSelectiveInheritance(t *testing.T) {
	handles, ids, handleArg := securitySentinels(t)
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sid := user.User.Sid.String()
	sddl, err := protocol.UserPipeSDDL(sid)
	if err != nil {
		t.Fatal(err)
	}
	ln, name := securityListener(t, sddl)
	reports := securityReports(ln, sid)
	t.Setenv("WINUNITD_SECURITY_BROKER_ONLY", "canary")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-winunitd-helper=security-report", handleArg, "--security-report-pipe="+name)
	cmd.SysProcAttr = &windows.SysProcAttr{HideWindow: true, AdditionalInheritedHandles: []syscall.Handle{syscall.Handle(handles[0])}}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	r := awaitSecurityReport(t, reports).report
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if len(r.Files) != 2 || r.Files[0] != ids[0] || r.Files[1] == ids[1] || !r.BrokerEnv || r.SID != sid {
		t.Fatal("positive control failed to identify selectively inherited file/environment")
	}
}

func TestNativeInteractiveUserManagerSecurity(t *testing.T) {
	if os.Getenv("WINUNITD_NATIVE_OVERLAP_SESSION") == "" {
		t.Skip("requires disposable SYSTEM/WTS qualification fixture")
	}
	tok, session, broker := overlapToken(t)
	testNativeUserManagerSecurity(t, tok, session, broker)
}

func TestNativeHeadlessUserManagerSecurity(t *testing.T) {
	tok, broker := headlessOverlapToken(t)
	testNativeUserManagerSecurity(t, tok, 0, broker)
	deadline := time.Now().Add(15 * time.Second)
	for overlapProfileLoaded(t, tok.Info.SID) {
		if time.Now().After(deadline) {
			t.Fatal("headless profile remained loaded after cleanup")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func testNativeUserManagerSecurity(t *testing.T, tok *runtime.UserToken, session uint32, broker *runtime.DaemonJob) {
	t.Helper()
	_, ids, handleArg := securitySentinels(t)
	t.Setenv("WINUNITD_SECURITY_BROKER_ONLY", "must-not-reach-child")
	sddl, err := protocol.UserPipeSDDL(tok.Info.SID)
	if err != nil {
		t.Fatal(err)
	}
	ln, name := securityListener(t, sddl)
	reports := securityReports(ln, tok.Info.SID)
	control := securityDeniedEndpoint(t, protocol.ControlPipeSDDL)
	maintenance := securityDeniedEndpoint(t, protocol.ControlPipeSDDL)
	other := os.Getenv("WINUNITD_NATIVE_SECURITY_OTHER_SID")
	if other == "" || other == tok.Info.SID {
		t.Fatal("distinct disposable peer SID required")
	}
	otherSDDL, err := protocol.UserPipeSDDL(other)
	if err != nil {
		t.Fatal(err)
	}
	peer := securityDeniedEndpoint(t, otherSDDL)
	proc, err := runtime.StartUserManager(runtime.UserManagerSpec{
		SID: tok.Info.SID, Token: tok, Exe: os.Args[0], Daemon: broker, LoadProfile: true,
		ExtraArgs: []string{"-winunitd-helper=security-report", handleArg, "--security-report-pipe=" + name,
			"--denied-pipes=" + strings.Join([]string{control, maintenance, peer}, ",")},
	})
	if proc != nil {
		t.Cleanup(func() {
			if err := proc.Kill(); err != nil {
				t.Error(err)
			}
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	result := awaitSecurityReport(t, reports)
	r := result.report
	if r.PID != uint32(proc.PID()) || r.SID != tok.Info.SID || r.Session != session || r.Elevated != 0 {
		t.Fatal("child token/process identity mismatch or elevated child")
	}
	if len(r.Files) != len(ids) {
		t.Fatal("missing sentinel handle observations")
	}
	for i, id := range ids {
		if r.Files[i] == id {
			t.Fatalf("child inherited broker sentinel %d", i)
		}
	}
	if r.BrokerEnv || !r.DefaultStdio || !r.WritableStdio || !r.Folders {
		t.Fatalf("child isolation: broker-env=%t default-stdio=%t writable-stdio=%t folders=%t", r.BrokerEnv, r.DefaultStdio, r.WritableStdio, r.Folders)
	}
	if len(r.Denied) != 3 || !r.Denied[0] || !r.Denied[1] || !r.Denied[2] {
		t.Fatalf("control/maintenance/other-user ACL denials: %v", r.Denied)
	}
	for _, name := range []string{control, maintenance, peer} {
		securityEndpointHealthy(t, name)
	}
	p := result.peer
	if p.SID != tok.Info.SID || !p.Owner || !p.Allowed() || p.CanLinger() || p.Administrator || p.LocalSystem {
		t.Fatal("actual child pipe token did not authorize as owner only")
	}
	if os.Getenv("WINUNITD_NATIVE_SECURITY_FILTERED") == "1" {
		// TOKEN_ELEVATION_TYPE.TokenElevationTypeLimited = 3.
		if r.ElevationType != 3 || r.AdminFlags&windows.SE_GROUP_USE_FOR_DENY_ONLY == 0 || r.AdminFlags&windows.SE_GROUP_ENABLED != 0 {
			t.Fatal("genuine UAC-limited administrator token required")
		}
	} else if r.AdminFlags != 0 {
		t.Fatal("standard-user fixture unexpectedly contains Administrators group")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := proc.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if err := proc.Kill(); err != nil {
		t.Fatal(err)
	}
	t.Logf("native child security passed: session=%d elevation-type=%d admin-flags=%#x file-sentinels=2 denied-endpoints=3 owner-only=true", r.Session, r.ElevationType, r.AdminFlags)
}
