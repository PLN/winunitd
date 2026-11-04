//go:build windows

package journal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func journalTestUser(t *testing.T) *windows.SID {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return user.User.Sid
}

// permissiveJournalParent models a manual installation below a directory
// whose inheritable DACL lets ordinary users and Everyone read new files.
func permissiveJournalParent(t *testing.T) string {
	t.Helper()
	parent := filepath.Join(t.TempDir(), "permissive")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	user := journalTestUser(t)
	sddl := "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;" + user.String() + ")(A;OICI;FR;;;BU)(A;OICI;FR;;;WD)"
	setTestDACL(t, parent, sddl)
	probe := filepath.Join(parent, "probe.txt")
	if err := os.WriteFile(probe, []byte("probe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !dACLAllowsWellKnown(t, probe, windows.WinBuiltinUsersSid) {
		t.Fatal("test parent does not grant inherited Users access")
	}
	return parent
}

func setTestDACL(t *testing.T, path, sddl string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
}

func dACLAllowsWellKnown(t *testing.T, path string, kind windows.WELL_KNOWN_SID_TYPE) bool {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		t.Fatalf("missing DACL: %v", err)
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			t.Fatal(err)
		}
		if ace.Header.AceType == windows.ACCESS_ALLOWED_ACE_TYPE && (*windows.SID)(unsafe.Pointer(&ace.SidStart)).IsWellKnown(kind) {
			return true
		}
	}
	return false
}

// assertJournalFileProtected requires the daemon-log principal set on a
// protected DACL and the process user, not a group or other identity, as owner.
func assertJournalFileProtected(t *testing.T, path string) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("%s: %v", filepath.Base(path), err)
	}
	user := journalTestUser(t)
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.Equals(user) {
		t.Fatalf("%s owner = %v, want process user (err=%v)", filepath.Base(path), owner, err)
	}
	assertDaemonDACL(t, sd, user)
}

func TestJournalFileSDDL(t *testing.T) {
	for _, tc := range []struct{ name, sid string }{
		{"system", "S-1-5-18"},
		{"user", "S-1-5-21-100-200-300-1001"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sid, err := windows.StringToSid(tc.sid)
			if err != nil {
				t.Fatal(err)
			}
			sd, err := windows.SecurityDescriptorFromString(journalFileSDDL(sid))
			if err != nil {
				t.Fatal(err)
			}
			owner, _, err := sd.Owner()
			if err != nil || owner == nil || !owner.Equals(sid) {
				t.Fatalf("owner = %v, want %s (err=%v)", owner, tc.sid, err)
			}
			assertDaemonDACL(t, sd, sid)
		})
	}
}

func TestJournalFilesProtectedUnderPermissiveParent(t *testing.T) {
	s, err := Open(filepath.Join(permissiveJournalParent(t), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.maxSize = 1
	const name = "acl.service"
	messages := []string{"one", "two", "three", "four", "five"}
	for _, message := range messages {
		if err := s.append(Entry{Unit: name, Message: message}); err != nil {
			t.Fatal(err)
		}
		if err := s.syncUnit(name); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range s.logPaths(name) {
		assertJournalFileProtected(t, path)
	}
	entries, err := s.Read(name)
	if err != nil || len(entries) != DefaultKeep+1 {
		t.Fatalf("history = %v, %v", entries, err)
	}
	for i, want := range messages[1:] {
		if entries[i].Message != want {
			t.Fatalf("record %d = %q, want %q", i, entries[i].Message, want)
		}
	}
	assertDiskAccounting(t, s)
}

func TestJournalRepairsExistingFiles(t *testing.T) {
	dir := filepath.Join(permissiveJournalParent(t), "journal")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const name = "legacy.service"
	current := filepath.Join(dir, unitFileName(name))
	archive := current + ".2"
	legacy := `{"v":4,"timestamp":"2026-01-01T00:00:00Z","unit":"legacy.service","message":"legacy","severity":"","session":"","userSid":""}` + "\n"
	for _, path := range []string{current, archive} {
		if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
			t.Fatal(err)
		}
		if !dACLAllowsWellKnown(t, path, windows.WinBuiltinUsersSid) {
			t.Fatal("legacy file did not inherit Users access")
		}
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.append(Entry{Unit: name, Message: "new"}); err != nil {
		t.Fatal(err)
	}
	if err := s.syncUnit(name); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{current, archive} {
		assertJournalFileProtected(t, path)
	}
	body, err := os.ReadFile(current)
	if err != nil || !strings.HasPrefix(string(body), legacy) || !strings.Contains(string(body), `"message":"new"`) {
		t.Fatalf("current journal lost history: %q, %v", body, err)
	}
	if body, err := os.ReadFile(archive); err != nil || string(body) != legacy {
		t.Fatalf("archive changed: %q, %v", body, err)
	}
}

// A planted name must not let the store relabel a file outside its namespace.
func TestJournalRejectsLinkedNames(t *testing.T) {
	for _, tc := range []struct {
		name, suffix string
		symlink      bool
	}{
		{"current hard link", "", false},
		{"archive hard link", ".1", false},
		{"current symlink", "", true},
		{"archive symlink", ".1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := permissiveJournalParent(t)
			dir := filepath.Join(parent, "journal")
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(parent, "target.txt")
			if err := os.WriteFile(target, []byte("target"), 0o644); err != nil {
				t.Fatal(err)
			}
			const unit = "linked.service"
			link := filepath.Join(dir, unitFileName(unit)) + tc.suffix
			if tc.symlink {
				if err := os.Symlink(target, link); err != nil {
					t.Skipf("symlink creation unavailable: %v", err)
				}
			} else if err := os.Link(target, link); err != nil {
				t.Fatal(err)
			}
			s, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			if err := s.append(Entry{Unit: unit, Message: "x"}); err == nil {
				t.Fatal("journal accepted a linked file")
			}
			if !dACLAllowsWellKnown(t, target, windows.WinBuiltinUsersSid) {
				t.Fatal("journal relabelled the link target")
			}
			if body, err := os.ReadFile(target); err != nil || string(body) != "target" {
				t.Fatalf("link target changed: %q, %v", body, err)
			}
		})
	}
}

func TestJournalNonAdminIdentity(t *testing.T) {
	dir := filepath.Join(permissiveJournalParent(t), "journal")
	withoutAdministrators(t, func() {
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		s.maxSize = 1
		const name = "nonadmin.service"
		for i := range DefaultKeep + 2 {
			if err := s.append(Entry{Unit: name, Message: fmt.Sprint(i)}); err != nil {
				t.Fatal(err)
			}
			if err := s.syncUnit(name); err != nil {
				t.Fatal(err)
			}
		}
		for _, path := range s.logPaths(name) {
			assertJournalFileProtected(t, path)
		}
		// Reopen through a fresh store on the restricted token.
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		again, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = again.Close() })
		if err := again.append(Entry{Unit: name, Message: "reopen"}); err != nil {
			t.Fatal(err)
		}
		if err := again.syncUnit(name); err != nil {
			t.Fatal(err)
		}
		assertJournalFileProtected(t, again.path(name))
	})
}

// A SYSTEM manager thread that is accidentally impersonating a user must not
// create a journal that user owns, since the owner keeps implicit WRITE_DAC.
// The explicit LocalSystem owner is absent from the user's token, so Windows
// refuses the create. Ordinary user tokens accept their own deny-only SID as
// owner, so only the LocalSystem lane can observe this.
func TestJournalSystemImpersonationCannotOwnNewFile(t *testing.T) {
	if !journalTestUser(t).IsWellKnown(windows.WinLocalSystemSid) {
		t.Skip("requires LocalSystem lane with an interactive console user")
	}
	var userToken windows.Token
	if err := windows.WTSQueryUserToken(windows.WTSGetActiveConsoleSessionId(), &userToken); err != nil {
		t.Skipf("no interactive console user token: %v", err)
	}
	defer userToken.Close()
	dir := t.TempDir()
	// Let the user create files here so only the owner rule can refuse it.
	setTestDACL(t, dir, "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;AU)")
	path := filepath.Join(dir, unitFileName("impersonated.service"))
	var impersonation windows.Token
	if err := windows.DuplicateTokenEx(userToken, windows.TOKEN_IMPERSONATE|windows.TOKEN_QUERY, nil, windows.SecurityImpersonation, windows.TokenImpersonation, &impersonation); err != nil {
		t.Fatal(err)
	}
	defer impersonation.Close()
	var openErr error
	func() {
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
		var f *os.File
		f, openErr = openJournalFile(path)
		if f != nil {
			_ = f.Close()
		}
	}()
	if !errors.Is(openErr, windows.ERROR_INVALID_OWNER) {
		t.Fatalf("impersonated open error = %v, want ERROR_INVALID_OWNER", openErr)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("impersonated create left a file: %v", err)
	}
}
