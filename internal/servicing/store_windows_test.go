//go:build windows

package servicing

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"github.com/PLN/winunitd/internal/servicing/servicingtest"
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
	return FloorPath(servicingtest.DataRoot(t))
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
	// The lock the write created beside the record has the same protection.
	for _, file := range []string{path, filepath.Join(filepath.Dir(path), LockFileName)} {
		requireMachineProtection(t, file)
	}
	if f, err := ReadFloor(path); err != nil || f.MinVersion != "0.1.0" {
		t.Fatalf("read back %+v %v", f, err)
	}
}

func requireMachineProtection(t *testing.T, path string) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := sd.Owner()
	if err != nil || !owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) && !owner.IsWellKnown(windows.WinLocalSystemSid) {
		t.Fatalf("%s owner %v: %v", path, owner, err)
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("%s DACL is not protected: %#x %v", path, control, err)
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		t.Fatalf("%s has no DACL", path)
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			t.Fatal(err)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsWellKnown(windows.WinLocalSystemSid) && !sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
			t.Fatalf("%s grants %s", path, sid)
		}
	}
}

// A lock file that others may write fails every change closed; readers
// do not take the lock.
func TestWindowsFloorRejectsWritableLock(t *testing.T) {
	requireElevated(t)
	path := nativeFloorPath(t)
	if err := WriteFloor(path, &Floor{Schema: 1, MinVersion: "0.1.0"}); err != nil {
		t.Fatal(err)
	}
	setSecurity(t, filepath.Join(filepath.Dir(path), LockFileName), "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FW;;;BU)", windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION)
	if err := WriteFloor(path, &Floor{Schema: 1, MinVersion: "0.2.0"}); err == nil || !strings.Contains(err.Error(), "floor lock permits non-administrator writes") {
		t.Fatalf("write under a writable lock: %v", err)
	}
	if err := RemoveFloor(path); err == nil {
		t.Fatal("removed under a writable lock")
	}
	if f, err := ReadFloor(path); err != nil || f.MinVersion != "0.1.0" {
		t.Fatalf("read %+v %v", f, err)
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
	elsewhere := filepath.Join(servicingtest.DataRoot(t), "daemon")
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
	base := servicingtest.Root(t)
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

// A non-elevated writer keeps access to its own record in its own test
// directory; the record grants nobody else write access.
func TestWindowsFloorRecordRoundTripsForTheWriter(t *testing.T) {
	path := nativeFloorPath(t)
	if err := WriteFloor(path, &Floor{Schema: 1, MinVersion: "0.1.0"}); err != nil {
		t.Fatal(err)
	}
	if f, err := ReadFloor(path); err != nil || f.MinVersion != "0.1.0" {
		t.Fatalf("read back %+v %v", f, err)
	}
	if err := RemoveFloor(path); err != nil {
		t.Fatal(err)
	}
}

// Each level of the record's directory chain is checked on its handle
// before a record is read or its absence trusted. Whether a standard or
// filtered token can actually delete or rename through a level is a native
// installed-product case; these tests cover the descriptors that allow it.
func TestWindowsFloorChainRejectsUnsafeLevels(t *testing.T) {
	floor := &Floor{Schema: 1, MinVersion: "0.1.0"}
	check := func(name, path, want string) {
		t.Helper()
		if f, err := ReadFloor(path); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: read %+v %v", name, f, err)
		}
		if hold := AdmissionHold(path, Build{Version: "9.9.9"}); !strings.HasPrefix(hold, "compatibility floor record is unusable: ") {
			t.Errorf("%s: hold %q", name, hold)
		}
		if err := WriteFloor(path, floor); err == nil {
			t.Errorf("%s: wrote a record", name)
		}
		if err := RemoveFloor(path); err == nil {
			t.Errorf("%s: removed a record", name)
		}
	}
	fresh := func() (string, string) {
		t.Helper()
		base := servicingtest.DataRoot(t)
		path := FloorPath(base)
		if err := WriteFloor(path, floor); err != nil {
			t.Fatal(err)
		}
		return base, path
	}
	// Users may delete entries of the data root: the daemon directory and
	// the record in it could be removed.
	base, path := fresh()
	servicingtest.SetDACL(t, base, servicingtest.ProtectedSDDL(t, "(A;;0x40;;;BU)"))
	check("data root delete-child", path, "data root permits")
	// Users may delete or rename the daemon directory itself.
	base, path = fresh()
	servicingtest.SetDACL(t, filepath.Join(base, "daemon"), servicingtest.ProtectedSDDL(t, "(A;;SD;;;BU)"))
	check("daemon directory delete", path, "daemon directory permits")
	// An inherit-only grant would make a future record writable.
	base, path = fresh()
	servicingtest.SetDACL(t, filepath.Join(base, "daemon"), servicingtest.ProtectedSDDL(t, "(A;OICIIO;FW;;;BU)"))
	check("daemon directory inherit-only write", path, "daemon directory permits")
	// Users may re-permission the data root.
	base, path = fresh()
	servicingtest.SetDACL(t, base, servicingtest.ProtectedSDDL(t, "(A;;WD;;;BU)"))
	check("data root write-DAC", path, "data root permits")

	// The containing directory may let users create entries, as
	// ProgramData does, but not delete or rename them.
	container := servicingtest.Root(t)
	base = filepath.Join(container, "winunitd")
	if err := os.MkdirAll(filepath.Join(base, "daemon"), 0o755); err != nil {
		t.Fatal(err)
	}
	servicingtest.Protect(t, base)
	path = FloorPath(base)
	if err := WriteFloor(path, floor); err != nil {
		t.Fatal(err)
	}
	servicingtest.SetDACL(t, container, servicingtest.ProtectedSDDL(t, "(A;CI;0x6;;;BU)(A;OICIIO;FA;;;CO)"))
	if f, err := ReadFloor(path); err != nil || f == nil {
		t.Fatalf("container like ProgramData: %+v %v", f, err)
	}
	servicingtest.SetDACL(t, container, servicingtest.ProtectedSDDL(t, "(A;;0x40;;;BU)"))
	check("container delete-child", path, "containing directory permits")
	// An unsafe container also blocks reading absence as "no floor".
	empty := servicingtest.Root(t)
	servicingtest.SetDACL(t, empty, servicingtest.ProtectedSDDL(t, "(A;;0x40;;;BU)"))
	check("container delete-child without a data root", FloorPath(filepath.Join(empty, "winunitd")), "containing directory permits")
}

func TestWindowsFloorChainRejectsUntrustedOwnerAndLinks(t *testing.T) {
	requireElevated(t)
	base := servicingtest.DataRoot(t)
	path := FloorPath(base)
	if err := WriteFloor(path, &Floor{Schema: 1, MinVersion: "0.1.0"}); err != nil {
		t.Fatal(err)
	}
	restore := enablePrivilege(t, "SeRestorePrivilege")
	setSecurity(t, base, "O:BU", windows.OWNER_SECURITY_INFORMATION)
	restore()
	if _, err := ReadFloor(path); err == nil || !strings.Contains(err.Error(), "data root must be owned") {
		t.Fatalf("data root owned by Users: %v", err)
	}
	// A linked data root is refused, not followed.
	elsewhere := servicingtest.DataRoot(t)
	if err := WriteFloor(FloorPath(elsewhere), &Floor{Schema: 1, MinVersion: "0.0.1"}); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(servicingtest.Root(t), "winunitd")
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFloor(FloorPath(link)); err == nil || !strings.Contains(err.Error(), "reparse") {
		t.Fatalf("linked data root: %v", err)
	}
}
