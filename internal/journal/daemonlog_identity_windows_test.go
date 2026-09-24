//go:build windows

package journal

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestDaemonLogNonAdminIdentity(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if user.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		t.Skip("requires user-token lane: disabling Administrators cannot remove LocalSystem identity")
	}
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY, &token); err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	admin, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	disabled := windows.SIDAndAttributes{Sid: admin}
	var restricted windows.Token
	// Match the protocol ACL regression: disable privileges and make the
	// Administrators group deny-only while preserving the user's own SID.
	create := windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateRestrictedToken")
	ok, _, callErr := create.Call(uintptr(token), 1, 1, uintptr(unsafe.Pointer(&disabled)), 0, 0, 0, 0, uintptr(unsafe.Pointer(&restricted)))
	if ok == 0 {
		t.Fatalf("CreateRestrictedToken: %v", callErr)
	}
	defer restricted.Close()
	var impersonation windows.Token
	if err := windows.DuplicateTokenEx(restricted, windows.TOKEN_IMPERSONATE|windows.TOKEN_QUERY, nil, windows.SecurityImpersonation, windows.TokenImpersonation, &impersonation); err != nil {
		t.Fatal(err)
	}
	defer impersonation.Close()
	// Keep all open/rotate/reopen access checks on the impersonating thread.
	// The background writer uses the already-open handle, as in production.
	runtime.LockOSThread()
	if err := windows.SetThreadToken(nil, impersonation); err != nil {
		runtime.UnlockOSThread()
		t.Fatal(err)
	}
	defer func() {
		if err := windows.RevertToSelf(); err != nil {
			// Do not return a still-impersonating thread to the Go thread pool.
			t.Errorf("RevertToSelf: %v", err)
			return
		}
		runtime.UnlockOSThread()
	}()
	testDaemonLogNonAdminWrites(t)
}

func testDaemonLogNonAdminWrites(t *testing.T) {
	t.Helper()
	var token windows.Token
	if err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &token); err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	groups, err := token.GetTokenGroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range groups.AllGroups() {
		if group.Sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) && group.Attributes&windows.SE_GROUP_ENABLED != 0 {
			t.Fatal("Administrators must not grant access to the caller")
		}
	}
	root := t.TempDir()
	// Cover a fresh directory, restart, and the directory left by the old
	// startup failure. Its owner can repair the DACL through WRITE_DAC.
	for pass := range 3 {
		if pass == 2 {
			sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;SY)(A;;FA;;;BA)")
			if err != nil {
				t.Fatal(err)
			}
			acl, _, err := sd.DACL()
			if err != nil {
				t.Fatal(err)
			}
			if err := windows.SetNamedSecurityInfo(filepath.Join(root, DaemonDirName), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
				t.Fatal(err)
			}
		}
		log, err := OpenDaemonLog(root, nil)
		if err != nil {
			t.Fatalf("open daemon log: %v", err)
		}
		t.Cleanup(func() { _ = log.CloseContext(context.Background()) })
		// Only this helper drives the sink; the writer has no queued records.
		// writeOne exercises real rotation without depending on queue timing.
		line := bytes.Repeat([]byte("x"), maxDaemonRecordBytes-1)
		for written := 0; written < maxDaemonFileBytes+len(line); written += len(line) {
			if err := log.writeOne(line); err != nil {
				t.Fatal(err)
			}
		}
		log.Record(DaemonEvent{Code: DaemonEventOpen})
		if err := log.CloseContext(context.Background()); err != nil {
			t.Fatal(err)
		}
		if log.Stats().Errors != 0 {
			t.Fatal("daemon writer reported errors")
		}
		body, err := os.ReadFile(log.path)
		if err != nil || !bytes.Contains(body, []byte(DaemonEventOpen)) {
			t.Fatalf("missing persisted daemon event: %v", err)
		}
		if body, err := os.ReadFile(log.archive); err != nil || len(body) == 0 {
			t.Fatalf("missing rotated log: %v", err)
		}
		assertProtectedDaemonDir(t, filepath.Join(root, DaemonDirName))
		user, err := windows.GetCurrentProcessToken().GetTokenUser()
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{log.path, log.archive} {
			sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
			if err != nil {
				t.Fatal(err)
			}
			assertDaemonDACL(t, sd, user.User.Sid)
		}
	}
	t.Log("non-admin create/write/rotate/reopen passed")
}
