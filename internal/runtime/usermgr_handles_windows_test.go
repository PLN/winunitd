//go:build windows

package runtime_test

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
	"golang.org/x/sys/windows"
)

// objectSentinels creates two inheritable sentinels of each non-file kind,
// held and closed by this test, and returns their handles, identities and
// the child argument naming them.
func objectSentinels(t *testing.T) ([]windows.Handle, []runtime.ObjectIdentity, string) {
	t.Helper()
	var handles []windows.Handle
	var ids []runtime.ObjectIdentity
	var refs []runtime.ObjectSentinelRef
	for _, kind := range []string{runtime.SentinelEvent, runtime.SentinelEvent, runtime.SentinelSection, runtime.SentinelSection} {
		h, id, err := runtime.NewObjectSentinel(kind)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = windows.CloseHandle(h) })
		handles, ids = append(handles, h), append(ids, id)
		refs = append(refs, runtime.ObjectSentinelRef{Kind: kind, Handle: uint64(h)})
	}
	return handles, ids, "--object-sentinels=" + runtime.FormatObjectSentinels(refs)
}

// This positive control explicitly inherits only the first sentinel of each
// kind. It proves the child detects an inherited event and section by
// object identity and tells them from an inheritable sibling it did not
// receive.
func TestNativeObjectProbeDetectsSelectiveInheritance(t *testing.T) {
	handles, ids, objectArg := objectSentinels(t)
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
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-winunitd-helper=security-report", objectArg, "--security-report-pipe="+name)
	cmd.SysProcAttr = &windows.SysProcAttr{HideWindow: true,
		AdditionalInheritedHandles: []syscall.Handle{syscall.Handle(handles[0]), syscall.Handle(handles[2])}}
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
	inherited, err := runtime.InheritedSentinels(ids, r.Objects)
	if err != nil {
		t.Fatal(err)
	}
	if r.SID != sid || !inherited[0] || inherited[1] || !inherited[2] || inherited[3] {
		t.Fatalf("positive control: inherited event/event/section/section = %v", inherited)
	}
}

func TestNativeInteractiveUserManagerObjectIsolation(t *testing.T) {
	if os.Getenv("WINUNITD_NATIVE_OVERLAP_SESSION") == "" {
		t.Skip("requires disposable SYSTEM/WTS qualification fixture")
	}
	tok, session, broker := overlapToken(t)
	testNativeUserManagerObjectIsolation(t, tok, session, broker)
}

func TestNativeHeadlessUserManagerObjectIsolation(t *testing.T) {
	tok, broker := headlessOverlapToken(t)
	testNativeUserManagerObjectIsolation(t, tok, 0, broker)
	deadline := time.Now().Add(15 * time.Second)
	for overlapProfileLoaded(t, tok.Info.SID) {
		if time.Now().After(deadline) {
			t.Fatal("headless profile remained loaded after cleanup")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// testNativeUserManagerObjectIsolation launches the production user manager
// with the security helper and inheritable event and section sentinels in
// the broker. The child must hold none of them. Its token identity is
// checked; the file, environment, stdio and pipe-denial cases are the
// separately qualified security test's and do not run here.
func testNativeUserManagerObjectIsolation(t *testing.T, tok *runtime.UserToken, session uint32, broker *runtime.DaemonJob) {
	t.Helper()
	_, ids, objectArg := objectSentinels(t)
	sddl, err := protocol.UserPipeSDDL(tok.Info.SID)
	if err != nil {
		t.Fatal(err)
	}
	ln, name := securityListener(t, sddl)
	reports := securityReports(ln, tok.Info.SID)
	proc, err := runtime.StartUserManager(runtime.UserManagerSpec{
		SID: tok.Info.SID, Token: tok, Exe: os.Args[0], Daemon: broker, LoadProfile: true,
		ExtraArgs: []string{"-winunitd-helper=security-report", objectArg, "--security-report-pipe=" + name},
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
	if r.PID != uint32(proc.PID()) || r.SID != tok.Info.SID || r.Session != session || r.Elevated != 0 || result.peer.SID != tok.Info.SID {
		t.Fatal("child token/process identity mismatch or elevated child")
	}
	if os.Getenv("WINUNITD_NATIVE_SECURITY_FILTERED") == "1" {
		// TOKEN_ELEVATION_TYPE.TokenElevationTypeLimited = 3.
		if r.ElevationType != 3 || r.AdminFlags&windows.SE_GROUP_USE_FOR_DENY_ONLY == 0 || r.AdminFlags&windows.SE_GROUP_ENABLED != 0 {
			t.Fatal("genuine UAC-limited administrator token required")
		}
	} else if r.AdminFlags != 0 {
		t.Fatal("standard-user fixture unexpectedly contains Administrators group")
	}
	inherited, err := runtime.InheritedSentinels(ids, r.Objects)
	if err != nil {
		t.Fatal(err)
	}
	for i, got := range inherited {
		if got {
			t.Fatalf("child inherited broker %s sentinel %d", ids[i].Kind, i)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := proc.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if err := proc.Kill(); err != nil {
		t.Fatal(err)
	}
	t.Logf("native child object isolation passed: session=%d elevation-type=%d event-sentinels=2 section-sentinels=2", r.Session, r.ElevationType)
}
