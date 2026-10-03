//go:build windows

package servicing

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The floor record's owner and DACL are only meaningful when the writer may
// assign Administrators as owner: an elevated administrator or SYSTEM.
func requireElevated(t *testing.T) {
	t.Helper()
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("floor protection tests need an elevated administrator or SYSTEM")
	}
}

func nativeFloorPath(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "daemon"), 0o755); err != nil {
		t.Fatal(err)
	}
	return FloorPath(base)
}

func setSecurity(t *testing.T, path, sddl string, info windows.SECURITY_INFORMATION) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	var owner *windows.SID
	var dacl *windows.ACL
	if info&windows.OWNER_SECURITY_INFORMATION != 0 {
		if owner, _, err = sd.Owner(); err != nil {
			t.Fatal(err)
		}
	}
	if info&windows.DACL_SECURITY_INFORMATION != 0 {
		if dacl, _, err = sd.DACL(); err != nil {
			t.Fatal(err)
		}
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, info, owner, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsFloorRecordIsWrittenProtected(t *testing.T) {
	requireElevated(t)
	path := nativeFloorPath(t)
	if err := WriteFloor(path, &Floor{Schema: 1, MinVersion: "0.1.0"}); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := sd.Owner()
	if err != nil || !owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
		t.Fatalf("record owner %v: %v", owner, err)
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("record DACL is not protected: %#x %v", control, err)
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		t.Fatal("record has no DACL")
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			t.Fatal(err)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsWellKnown(windows.WinLocalSystemSid) && !sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
			t.Fatalf("record grants %s", sid)
		}
	}
	if f, err := ReadFloor(path); err != nil || f.MinVersion != "0.1.0" {
		t.Fatalf("read back %+v %v", f, err)
	}
}

func TestWindowsFloorRejectsWritableRecord(t *testing.T) {
	requireElevated(t)
	path := nativeFloorPath(t)
	if err := WriteFloor(path, &Floor{Schema: 1, MinVersion: "0.1.0"}); err != nil {
		t.Fatal(err)
	}
	// An inherited-only or direct write grant to Users makes it untrusted.
	setSecurity(t, path, "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FW;;;BU)", windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION)
	if _, err := ReadFloor(path); err == nil || !strings.Contains(err.Error(), "non-administrator") {
		t.Fatalf("writable record: %v", err)
	}
	if hold := AdmissionHold(path, Build{Version: "9.9.9"}); !strings.HasPrefix(hold, "compatibility floor record is unusable") {
		t.Fatalf("writable record hold %q", hold)
	}
}

func TestWindowsFloorRejectsUntrustedOwner(t *testing.T) {
	requireElevated(t)
	path := nativeFloorPath(t)
	if err := WriteFloor(path, &Floor{Schema: 1, MinVersion: "0.1.0"}); err != nil {
		t.Fatal(err)
	}
	restore := enablePrivilege(t, "SeRestorePrivilege")
	defer restore()
	setSecurity(t, path, "O:BU", windows.OWNER_SECURITY_INFORMATION)
	if _, err := ReadFloor(path); err == nil || !strings.Contains(err.Error(), "owned by") {
		t.Fatalf("record owned by Users: %v", err)
	}
}

func TestWindowsFloorRejectsReparsePoints(t *testing.T) {
	requireElevated(t)
	path := nativeFloorPath(t)
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Mkdir(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(elsewhere, FloorFileName)
	if err := WriteFloor(target, &Floor{Schema: 1, MinVersion: "0.0.1"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFloor(path); err == nil || !strings.Contains(err.Error(), "reparse") {
		t.Fatalf("linked record: %v", err)
	}
	// A redirected daemon directory is refused before the record is opened.
	base := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(base, "daemon")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFloor(FloorPath(base)); err == nil || !strings.Contains(err.Error(), "reparse") {
		t.Fatalf("linked directory: %v", err)
	}
	if err := WriteFloor(FloorPath(base), &Floor{Schema: 1, MinVersion: "0.1.0"}); err == nil {
		t.Fatal("wrote through a linked directory")
	}
}

// enablePrivilege enables a privilege the elevated token holds and returns
// the function that disables it again.
func enablePrivilege(t *testing.T, name string) func() {
	t.Helper()
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &tok); err != nil {
		t.Fatal(err)
	}
	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, windows.StringToUTF16Ptr(name), &luid); err != nil {
		_ = tok.Close()
		t.Fatal(err)
	}
	set := func(attrs uint32) error {
		tp := windows.Tokenprivileges{PrivilegeCount: 1}
		tp.Privileges[0] = windows.LUIDAndAttributes{Luid: luid, Attributes: attrs}
		return windows.AdjustTokenPrivileges(tok, false, &tp, 0, nil, nil)
	}
	if err := set(windows.SE_PRIVILEGE_ENABLED); err != nil {
		_ = tok.Close()
		t.Skipf("cannot enable %s: %v", name, err)
	}
	return func() {
		_ = set(0)
		_ = tok.Close()
	}
}
