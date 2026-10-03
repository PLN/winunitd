//go:build windows

package runtime_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
	"golang.org/x/sys/windows"
)

// firstOfEachKind is the explicit inherit list of the positive control:
// the first of the two sentinels of every kind.
func firstOfEachKind(handles []windows.Handle) []syscall.Handle {
	var out []syscall.Handle
	for i := 0; i < len(handles); i += 2 {
		out = append(out, syscall.Handle(handles[i]))
	}
	return out
}

// objectSentinels creates two inheritable sentinels of each non-file kind,
// the two of a kind next to each other, held and closed by this test, and
// returns their handles, identities and the child argument naming them.
func objectSentinels(t *testing.T) ([]windows.Handle, []runtime.ObjectIdentity, string) {
	t.Helper()
	var handles []windows.Handle
	var ids []runtime.ObjectIdentity
	var refs []runtime.ObjectSentinelRef
	for _, kind := range runtime.SentinelKinds {
		for range 2 {
			h, id, err := runtime.NewObjectSentinel(kind)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = windows.CloseHandle(h) })
			handles, ids = append(handles, h), append(ids, id)
			refs = append(refs, runtime.ObjectSentinelRef{Kind: kind, Handle: uint64(h)})
		}
	}
	return handles, ids, "--object-sentinels=" + runtime.FormatObjectSentinels(refs)
}

// This positive control explicitly inherits only the first sentinel of each
// kind. It proves the child detects an inherited event, section, mutex,
// semaphore and job by object identity and tells each from an inheritable
// sibling it did not receive.
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
		AdditionalInheritedHandles: firstOfEachKind(handles)}
	if err := cmd.Start(); err != nil {
		t.Fatal("start the security helper:", launchFailure(err))
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	r := awaitSecurityReport(t, reports).report
	if err := cmd.Wait(); err != nil {
		t.Fatal("the security helper:", launchFailure(err))
	}
	inherited, err := runtime.InheritedSentinels(ids, r.Objects)
	if err != nil {
		t.Fatal(err)
	}
	if r.SID != sid {
		t.Fatal("the helper reported another account")
	}
	for i, got := range inherited {
		if got != (i%2 == 0) {
			t.Fatalf("positive control: %s sentinel %d inherited=%t", ids[i].Kind, i, got)
		}
	}
}

func TestNativeInteractiveUserManagerObjectIsolation(t *testing.T) {
	if os.Getenv("WINUNITD_NATIVE_OVERLAP_SESSION") == "" {
		t.Skip("requires disposable SYSTEM/WTS qualification fixture")
	}
	tok, session, broker := overlapToken(t)
	testNativeUserManagerObjectIsolation(t, tok, session, broker, false)
}

func TestNativeHeadlessUserManagerObjectIsolation(t *testing.T) {
	tok, broker := headlessOverlapToken(t)
	testNativeUserManagerObjectIsolation(t, tok, 0, broker, true)
	deadline := time.Now().Add(15 * time.Second)
	for overlapProfileLoaded(t, tok.Info.SID) {
		if time.Now().After(deadline) {
			t.Fatal("headless profile remained loaded after cleanup")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// testNativeUserManagerObjectIsolation launches the production user manager
// with the security helper and inheritable sentinels of every kind in the
// broker. The child must hold none of them. Its token identity is
// checked; the file, environment, stdio and pipe-denial cases are the
// separately qualified security test's and do not run here. The child's
// own token, read from its process, must be the genuine one of its mode:
// the product's S4U logon in session zero, or an interactive logon in the
// selected session. When the qualification runner asks for it, the test
// reports that subject with this test process as its owner.
func testNativeUserManagerObjectIsolation(t *testing.T, tok *runtime.UserToken, session uint32, broker *runtime.DaemonJob, headless bool) {
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
		t.Fatal("start the user manager:", launchFailure(err))
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
	subject := objectSubject(t, uint32(proc.PID()))
	switch {
	case subject.Token.SID != tok.Info.SID || subject.Token.Session != session || subject.Token.AuthenticationID == "":
		t.Fatal("the child's own token is not the selected account's")
	case headless && (subject.Token.Source != "winunitd" || subject.Token.LogonType != 3 && subject.Token.LogonType != 4):
		t.Fatal("the headless child does not hold the product's S4U logon")
	case !headless && subject.Token.LogonType != 2 && subject.Token.LogonType != 10 && subject.Token.LogonType != 11:
		t.Fatal("the interactive child does not hold an interactive logon")
	}
	reportObjectSubject(t, subject)
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
	t.Logf("native child object isolation passed: session=%d elevation-type=%d kinds=%d sentinels=%d", r.Session, r.ElevationType, len(runtime.SentinelKinds), len(ids))
}

// objectSubjectEnv names the file a qualification runner asks for: the
// launched child's process incarnation and token, with this test process,
// the SYSTEM test owner, as its owner. The format is the qualification
// receipt's subject report.
const objectSubjectEnv = "WINUNITD_QUAL_SUBJECT_OUT"

type objectSubjectToken struct {
	SID              string `json:"sid"`
	Session          uint32 `json:"session"`
	Elevated         bool   `json:"elevated"`
	Source           string `json:"source"`
	LogonType        uint32 `json:"logonType,omitempty"`
	AuthPackage      string `json:"authPackage,omitempty"`
	AuthenticationID string `json:"authenticationId"`
	ElevationType    uint32 `json:"elevationType,omitempty"`
}

type objectSubjectReport struct {
	Test         string             `json:"test"`
	OwnerPID     uint32             `json:"ownerPid"`
	OwnerCreated uint64             `json:"ownerCreated"`
	PID          uint32             `json:"pid"`
	Created      uint64             `json:"created"`
	Token        objectSubjectToken `json:"token"`
}

var (
	objectSecur32             = windows.NewLazySystemDLL("secur32.dll")
	objectLsaGetLogonSession  = objectSecur32.NewProc("LsaGetLogonSessionData")
	objectLsaFreeReturnBuffer = objectSecur32.NewProc("LsaFreeReturnBuffer")
)

// objectSubject reads the child's incarnation and its own primary token.
// The user-manager handle keeps the process object, so the PID cannot name
// another process meanwhile.
func objectSubject(t *testing.T, pid uint32) objectSubjectReport {
	t.Helper()
	r := objectSubjectReport{Test: t.Name(), OwnerPID: windows.GetCurrentProcessId(), PID: pid}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		t.Fatal("open the child:", err)
	}
	defer windows.CloseHandle(h)
	if r.OwnerCreated, err = objectProcessCreated(windows.CurrentProcess()); err == nil {
		if r.Created, err = objectProcessCreated(h); err == nil {
			r.Token, err = objectTokenFacts(h)
		}
	}
	if err != nil {
		t.Fatal("the child's identity:", err)
	}
	return r
}

func objectProcessCreated(h windows.Handle) (uint64, error) {
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return 0, err
	}
	return uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime), nil
}

func objectTokenFacts(process windows.Handle) (objectSubjectToken, error) {
	var s objectSubjectToken
	var tok windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY|windows.TOKEN_QUERY_SOURCE, &tok); err != nil {
		return s, err
	}
	defer tok.Close()
	user, err := tok.GetTokenUser()
	if err != nil {
		return s, err
	}
	s.SID, s.Elevated = user.User.Sid.String(), tok.IsElevated()
	var n uint32
	if err := windows.GetTokenInformation(tok, windows.TokenSessionId, (*byte)(unsafe.Pointer(&s.Session)), 4, &n); err != nil {
		return s, err
	}
	if err := windows.GetTokenInformation(tok, windows.TokenElevationType, (*byte)(unsafe.Pointer(&s.ElevationType)), 4, &n); err != nil {
		return s, err
	}
	var source struct {
		Name [8]byte
		ID   windows.LUID
	}
	if err := windows.GetTokenInformation(tok, windows.TokenSource, (*byte)(unsafe.Pointer(&source)), uint32(unsafe.Sizeof(source)), &n); err != nil {
		return s, err
	}
	s.Source = strings.TrimRight(string(source.Name[:]), "\x00 ")
	var stats struct {
		TokenID            windows.LUID
		AuthenticationID   windows.LUID
		ExpirationTime     int64
		TokenType          uint32
		ImpersonationLevel uint32
		DynamicCharged     uint32
		DynamicAvailable   uint32
		GroupCount         uint32
		PrivilegeCount     uint32
		ModifiedID         windows.LUID
	}
	if err := windows.GetTokenInformation(tok, windows.TokenStatistics, (*byte)(unsafe.Pointer(&stats)), uint32(unsafe.Sizeof(stats)), &n); err != nil {
		return s, err
	}
	id := stats.AuthenticationID
	s.AuthenticationID = fmt.Sprintf("%08x:%08x", id.HighPart, id.LowPart)
	var data *struct {
		Size                  uint32
		LogonID               windows.LUID
		UserName              windows.NTUnicodeString
		LogonDomain           windows.NTUnicodeString
		AuthenticationPackage windows.NTUnicodeString
		LogonType             uint32
	}
	if r, _, _ := objectLsaGetLogonSession.Call(uintptr(unsafe.Pointer(&id)), uintptr(unsafe.Pointer(&data))); r != 0 || data == nil {
		return s, fmt.Errorf("logon session data: status %#x", r)
	}
	s.LogonType, s.AuthPackage = data.LogonType, data.AuthenticationPackage.String()
	objectLsaFreeReturnBuffer.Call(uintptr(unsafe.Pointer(data)))
	return s, nil
}

// reportObjectSubject writes the subject report when the runner asked for
// it. A requested report that cannot be written fails the test.
func reportObjectSubject(t *testing.T, r objectSubjectReport) {
	t.Helper()
	path := os.Getenv(objectSubjectEnv)
	if path == "" {
		return
	}
	data, err := json.Marshal(r)
	if err == nil {
		err = os.WriteFile(path, append(data, '\n'), 0o600)
	}
	if err != nil {
		t.Fatal("the qualification subject report could not be written")
	}
}
