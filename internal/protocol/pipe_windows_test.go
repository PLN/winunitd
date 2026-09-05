//go:build windows

package protocol

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func TestDialPipeUsesWinioIdentification(t *testing.T) {
	t.Parallel()
	if winio.PipeImpLevel(PipeDialImpLevel) != winio.PipeImpLevelIdentification {
		t.Fatalf("PipeDialImpLevel = 0x%x, want winio.PipeImpLevelIdentification 0x%x",
			PipeDialImpLevel, winio.PipeImpLevelIdentification)
	}
	if winio.PipeImpLevel(PipeDialImpLevel) == winio.PipeImpLevelAnonymous {
		t.Fatal("DialPipe must not use PipeImpLevelAnonymous")
	}
}

func TestListenPipeRejectsRemoteClients(t *testing.T) {
	requireElevatedControlPipe(t)
	name := fmt.Sprintf(`\\.\pipe\winunitd-flag-%d-%d`, os.Getpid(), time.Now().UnixNano())
	lis, client, server := listenDialKeep(t, func() (net.Listener, error) {
		return ListenPipe(name)
	}, name)
	defer lis.Close()
	defer client.Close()
	defer server.Close()

	if !pipeRejectsRemoteOn(t, client, server) {
		t.Fatal("PIPE_REJECT_REMOTE_CLIENTS / FILE_PIPE_REJECT_REMOTE_CLIENTS must be set")
	}
}

func TestListenPipeSDDLRejectsRemoteClients(t *testing.T) {
	sid, err := CurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	sddl, err := UserPipeSDDL(sid)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf(`\\.\pipe\winunitd-user-flag-%d-%d`, os.Getpid(), time.Now().UnixNano())
	lis, client, server := listenDialKeep(t, func() (net.Listener, error) {
		return ListenPipeSDDL(name, sddl)
	}, name)
	defer lis.Close()
	defer client.Close()
	defer server.Close()

	if !pipeRejectsRemoteOn(t, client, server) {
		t.Fatal("user-pipe create must set PIPE_REJECT_REMOTE_CLIENTS")
	}
}

func TestListenPipeFirstInstanceFailsIfNameTaken(t *testing.T) {
	name := fmt.Sprintf(`\\.\pipe\winunitd-first-%d-%d`, os.Getpid(), time.Now().UnixNano())
	lis, err := ListenPipe(name)
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()

	_, err = ListenPipe(name)
	if err == nil {
		t.Fatal("second ListenPipe on the same name must fail")
	}
	if !isPipeNameTaken(err) && !strings.Contains(strings.ToLower(err.Error()), "already taken") &&
		!strings.Contains(strings.ToLower(err.Error()), "already exists") {
		t.Fatalf("second listen error = %v, want first-instance / already taken", err)
	}
	if !strings.Contains(err.Error(), "already taken") {
		t.Fatalf("error should say the name is taken: %v", err)
	}
	if !strings.Contains(err.Error(), name) {
		t.Fatalf("error should name the pipe: %v", err)
	}
}

func TestListenPipeSDDLFirstInstanceFailsIfNameTaken(t *testing.T) {
	sid, err := CurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	sddl, err := UserPipeSDDL(sid)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf(`\\.\pipe\winunitd-user-first-%d-%d`, os.Getpid(), time.Now().UnixNano())
	lis, err := ListenPipeSDDL(name, sddl)
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()

	_, err = ListenPipeSDDL(name, sddl)
	if err == nil {
		t.Fatal("second ListenPipeSDDL on the same name must fail")
	}
	if !strings.Contains(err.Error(), "already taken") && !isPipeNameTaken(err) {
		t.Fatalf("second listen error = %v, want first-instance / already taken", err)
	}
}

func TestControlDaemonIdentityRejectsRestrictedToken(t *testing.T) {
	requireNonSystemTokenFixture(t)
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY, &tok); err != nil {
		t.Fatal(err)
	}
	defer tok.Close()

	adminSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	restricted, err := createRestrictedToken(tok, []windows.SIDAndAttributes{{Sid: adminSID}})
	if err != nil {
		t.Fatal(err)
	}
	defer restricted.Close()

	ok, got, err := controlDaemonIdentity(restricted)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("restricted (non-admin) token must not be a control daemon identity; owner=%s", got)
	}
}

func TestControlDaemonIdentityAcceptsCurrentProcess(t *testing.T) {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &tok); err != nil {
		t.Fatal(err)
	}
	defer tok.Close()

	ok, got, err := controlDaemonIdentity(tok)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Skipf("current process is not LocalSystem or Administrators (owner=%s); CI runners are", got)
	}
}

func TestClientRejectsMismatchedServerOwner(t *testing.T) {
	requireElevatedControlPipe(t)
	name := fmt.Sprintf(`\\.\pipe\winunitd-owner-%d-%d`, os.Getpid(), time.Now().UnixNano())
	lis, client, server := listenDialKeep(t, func() (net.Listener, error) {
		return ListenPipe(name)
	}, name)
	defer lis.Close()
	defer client.Close()
	defer server.Close()

	err := verifyUserServerOwner(client, "S-1-5-21-1-2-3-1001")
	if err == nil {
		t.Fatal("mismatched server-owner SID must refuse")
	}
	if !strings.Contains(err.Error(), "possible squat") && !strings.Contains(err.Error(), "user-manager identity") {
		t.Fatalf("reject error = %v, want squat / identity mismatch", err)
	}

	acceptErr := make(chan error, 1)
	go func() {
		c, err := lis.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		_ = c.Close()
		acceptErr <- nil
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	closed, err := dialVerified(ctx, name, func(c net.Conn) error {
		return verifyUserServerOwner(c, "S-1-5-21-1-2-3-1001")
	})
	if err == nil {
		_ = closed.Close()
		t.Fatal("dialVerified must not return a connection on owner mismatch")
	}
	if closed != nil {
		_ = closed.Close()
		t.Fatal("mismatch must close the connection; no RPC path")
	}
	select {
	case <-acceptErr:
	case <-time.After(5 * time.Second):
		t.Fatal("accept timed out after mismatch dial")
	}
}

func TestClientAcceptsMatchingUserServerOwner(t *testing.T) {
	sid, err := CurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	sddl, err := UserPipeSDDL(sid)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf(`\\.\pipe\winunitd-owner-ok-%d-%d`, os.Getpid(), time.Now().UnixNano())
	lis, client, server := listenDialKeep(t, func() (net.Listener, error) {
		return ListenPipeSDDL(name, sddl)
	}, name)
	defer lis.Close()
	defer client.Close()
	defer server.Close()

	if err := verifyUserServerOwner(client, sid); err != nil {
		t.Fatal(err)
	}
}

func TestClientAcceptsControlServerOwner(t *testing.T) {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &tok); err != nil {
		t.Fatal(err)
	}
	defer tok.Close()
	ok, got, err := controlDaemonIdentity(tok)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Skipf("current process is not LocalSystem or Administrators (owner=%s)", got)
	}

	name := fmt.Sprintf(`\\.\pipe\winunitd-ctrl-owner-%d-%d`, os.Getpid(), time.Now().UnixNano())
	lis, client, server := listenDialKeep(t, func() (net.Listener, error) {
		return ListenPipe(name)
	}, name)
	defer lis.Close()
	defer client.Close()
	defer server.Close()

	if err := verifyControlServerOwner(client); err != nil {
		t.Fatal(err)
	}
}

const filePipeLocalInformation = 24

type filePipeLocalInfo struct {
	NamedPipeType          uint32
	NamedPipeConfiguration uint32
	MaximumInstances       uint32
	CurrentInstances       uint32
	InboundQuota           uint32
	ReadDataAvailable      uint32
	OutboundQuota          uint32
	WriteQuotaAvailable    uint32
	NamedPipeState         uint32
	NamedPipeEnd           uint32
}

type ioStatusBlock struct {
	Status, Information uintptr
}

var procNtQueryInformationFile = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtQueryInformationFile")

func pipeRejectsRemoteOn(t *testing.T, client, server net.Conn) bool {
	t.Helper()
	for _, c := range []net.Conn{client, server} {
		h, ok := connHandle(c)
		if !ok {
			continue
		}
		if pipeRejectsRemoteClients(t, h) {
			return true
		}
	}
	return false
}

func pipeRejectsRemoteClients(t *testing.T, h windows.Handle) bool {
	t.Helper()
	var state uint32
	if err := windows.GetNamedPipeHandleState(h, &state, nil, nil, nil, nil, 0); err == nil {
		if state&windows.PIPE_REJECT_REMOTE_CLIENTS != 0 {
			return true
		}
	}
	var iosb ioStatusBlock
	var info filePipeLocalInfo
	r, _, _ := procNtQueryInformationFile.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(&iosb)),
		uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info),
		filePipeLocalInformation,
	)
	if r != 0 {
		t.Logf("NtQueryInformationFile FilePipeLocalInformation: NTSTATUS 0x%x", r)
		return false
	}
	t.Logf("NamedPipeType=0x%x", info.NamedPipeType)
	return info.NamedPipeType&windows.FILE_PIPE_REJECT_REMOTE_CLIENTS != 0
}

func listenDialKeep(t *testing.T, listen func() (net.Listener, error), name string) (net.Listener, net.Conn, net.Conn) {
	t.Helper()
	lis, err := listen()
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan net.Conn, 1)
	errc := make(chan error, 1)
	go func() {
		c, err := lis.Accept()
		if err != nil {
			errc <- err
			return
		}
		got <- c
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := DialPipe(ctx, name)
	if err != nil {
		lis.Close()
		t.Fatalf("dial: %v", err)
	}
	select {
	case srv := <-got:
		return lis, client, srv
	case err := <-errc:
		_ = client.Close()
		lis.Close()
		t.Fatal(err)
		return nil, nil, nil
	case <-time.After(5 * time.Second):
		_ = client.Close()
		lis.Close()
		t.Fatal("accept timed out")
		return nil, nil, nil
	}
}
