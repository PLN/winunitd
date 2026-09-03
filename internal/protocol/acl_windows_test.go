//go:build windows

package protocol

import (
	"context"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsPipeACLDeniesNonAdmin(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("requires an elevated process (UAC-filtered token cannot dial the control pipe)")
	}
	name := fmt.Sprintf(`\\.\pipe\winunitd-test-%d-%d`, os.Getpid(), time.Now().UnixNano())
	lis, err := ListenPipe(name)
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()

	acceptErr := make(chan error, 1)
	go func() {
		conn, err := lis.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		_ = conn.Close()
		acceptErr <- nil
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	adminConn, err := DialPipe(ctx, name)
	if err != nil {
		t.Fatalf("admin dial: %v", err)
	}
	select {
	case err := <-acceptErr:
		_ = adminConn.Close()
		if err != nil {
			t.Fatalf("admin accept: %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = adminConn.Close()
		t.Fatal("admin accept timed out")
	}

	// Leave an instance listening so the denied CreateFile hits the DACL
	// rather than ERROR_FILE_NOT_FOUND.
	go func() {
		conn, err := lis.Accept()
		if err != nil {
			return
		}
		_ = conn.Close()
	}()

	deadline := time.Now().Add(5 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		last = dialAsRestricted(name)
		if last == nil {
			t.Fatal("restricted (non-admin) token must be denied")
		}
		if isAccessDenied(last) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("restricted dial error = %v, want access denied", last)
}

func TestWindowsSecurityDescriptorParses(t *testing.T) {
	sd, err := windows.SecurityDescriptorFromString(ControlPipeSDDL)
	if err != nil {
		t.Fatal(err)
	}
	if sd == nil {
		t.Fatal("nil security descriptor")
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if dacl == nil {
		t.Fatal("missing DACL")
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("DACL should be protected (D:P)")
	}
}

func TestUserPipeSDDLParses(t *testing.T) {
	sid, err := CurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	sddl, err := UserPipeSDDL(sid)
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if dacl == nil {
		t.Fatal("missing DACL")
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("DACL should be protected (D:P)")
	}
}

func TestUserPipeSDDLAllowsOwner(t *testing.T) {
	sid, err := CurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	sddl, err := UserPipeSDDL(sid)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf(`\\.\pipe\winunitd-user-test-%d-%d`, os.Getpid(), time.Now().UnixNano())
	lis, err := ListenPipeSDDL(name, sddl)
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()

	acceptErr := make(chan error, 1)
	go func() {
		conn, err := lis.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		_ = conn.Close()
		acceptErr <- nil
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := DialPipe(ctx, name)
	if err != nil {
		t.Fatalf("owner dial: %v", err)
	}
	select {
	case err := <-acceptErr:
		_ = conn.Close()
		if err != nil {
			t.Fatalf("owner accept: %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = conn.Close()
		t.Fatal("owner accept timed out")
	}
}

var procCreateRestrictedToken = windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateRestrictedToken")

const disableMaxPrivilege = 0x1

func createRestrictedToken(existing windows.Token, disableSids []windows.SIDAndAttributes) (windows.Token, error) {
	var newToken windows.Token
	var sidPtr uintptr
	n := uint32(len(disableSids))
	if n > 0 {
		sidPtr = uintptr(unsafe.Pointer(&disableSids[0]))
	}
	r1, _, e1 := procCreateRestrictedToken.Call(
		uintptr(existing),
		uintptr(disableMaxPrivilege),
		uintptr(n),
		sidPtr,
		0, 0,
		0, 0,
		uintptr(unsafe.Pointer(&newToken)),
	)
	if r1 == 0 {
		if e1 != syscall.Errno(0) {
			return 0, e1
		}
		return 0, syscall.EINVAL
	}
	return newToken, nil
}

func dialAsRestricted(pipeName string) error {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY, &tok); err != nil {
		return err
	}
	defer tok.Close()

	adminSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	restricted, err := createRestrictedToken(tok, []windows.SIDAndAttributes{{Sid: adminSID}})
	if err != nil {
		return err
	}
	defer restricted.Close()

	var impersonation windows.Token
	if err := windows.DuplicateTokenEx(restricted, windows.TOKEN_IMPERSONATE|windows.TOKEN_QUERY, nil, windows.SecurityImpersonation, windows.TokenImpersonation, &impersonation); err != nil {
		return err
	}
	defer impersonation.Close()

	if err := windows.SetThreadToken(nil, impersonation); err != nil {
		return err
	}
	defer windows.RevertToSelf()

	path, err := windows.UTF16PtrFromString(pipeName)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(
		path,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return err
	}
	_ = windows.CloseHandle(h)
	return nil
}

func isAccessDenied(err error) bool {
	if err == nil {
		return false
	}
	if errorsIsErrno(err, windows.ERROR_ACCESS_DENIED) {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "access is denied") || strings.Contains(s, "access denied")
}

func errorsIsErrno(err error, errno windows.Errno) bool {
	for err != nil {
		if e, ok := err.(syscall.Errno); ok && windows.Errno(e) == errno {
			return true
		}
		if e, ok := err.(windows.Errno); ok && e == errno {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			break
		}
		err = u.Unwrap()
	}
	return false
}
