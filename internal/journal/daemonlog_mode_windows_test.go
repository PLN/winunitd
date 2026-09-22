//go:build windows

package journal

import (
	"os"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func assertProtectedDaemonDir(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		t.Fatalf("daemon directory = %v err=%v", info, err)
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("daemon directory DACL is not protected")
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		t.Fatalf("daemon directory DACL: %v", err)
	}
	var sawSystem, sawAdmin bool
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			t.Fatal(err)
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			t.Fatalf("unsupported daemon directory ACE type %d", ace.Header.AceType)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		switch {
		case sid.IsWellKnown(windows.WinLocalSystemSid):
			sawSystem = true
		case sid.IsWellKnown(windows.WinBuiltinAdministratorsSid):
			sawAdmin = true
		default:
			t.Fatalf("daemon directory allows SID %s", sid)
		}
	}
	if !sawSystem || !sawAdmin {
		t.Fatal("daemon directory DACL must allow SYSTEM and Administrators")
	}
}
