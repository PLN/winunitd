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
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	assertDaemonDACL(t, sd, user.User.Sid)
}

func assertDaemonDACL(t *testing.T, sd *windows.SECURITY_DESCRIPTOR, owner *windows.SID) {
	t.Helper()
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("unprotected DACL: %v", err)
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		t.Fatalf("missing DACL: %v", err)
	}
	want := 3
	if owner.IsWellKnown(windows.WinLocalSystemSid) {
		want = 2
	}
	if int(acl.AceCount) != want {
		t.Fatalf("ACE count = %d, want %d", acl.AceCount, want)
	}
	var sawSystem, sawAdmin, sawOwner bool
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			t.Fatal(err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			t.Fatalf("unsupported daemon directory ACE type %d", ace.Header.AceType)
		}
		if ace.Mask != 0x001f01ff /* FILE_ALL_ACCESS */ || ace.Header.AceFlags != 0 {
			t.Fatal("daemon ACE must grant full access without inheritance")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if sid.Equals(owner) {
			sawOwner = true
		}
		switch {
		case sid.IsWellKnown(windows.WinLocalSystemSid):
			sawSystem = true
		case sid.IsWellKnown(windows.WinBuiltinAdministratorsSid):
			sawAdmin = true
		case sid.Equals(owner):
		default:
			t.Fatal("daemon DACL allows an unrelated identity")
		}
	}
	if !sawSystem || !sawAdmin || !sawOwner {
		t.Fatal("daemon directory DACL must allow process owner, SYSTEM and Administrators")
	}
}

func TestDaemonPathSDDL(t *testing.T) {
	for _, tc := range []struct{ name, sid string }{
		{"system", "S-1-5-18"},
		{"user", "S-1-5-21-100-200-300-1001"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sid, err := windows.StringToSid(tc.sid)
			if err != nil {
				t.Fatal(err)
			}
			sd, err := windows.SecurityDescriptorFromString(daemonPathSDDL(sid))
			if err != nil {
				t.Fatal(err)
			}
			assertDaemonDACL(t, sd, sid)
		})
	}
}

func TestDaemonPathOwnerAllowed(t *testing.T) {
	const userSID = "S-1-5-21-100-200-300-1001"
	const otherSID = "S-1-5-21-100-200-300-1002"
	for _, tc := range []struct {
		name, owner, user string
		want              bool
	}{
		{"process user", userSID, userSID, true},
		{"system", "S-1-5-18", userSID, true},
		{"administrators", "S-1-5-32-544", userSID, true},
		{"foreign user", otherSID, userSID, false},
		{"users group", "S-1-5-32-545", userSID, false},
		{"system cannot adopt foreign owner", userSID, "S-1-5-18", false},
		{"missing owner", "", userSID, false},
		{"missing process identity", "S-1-5-18", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parse := func(value string) *windows.SID {
				if value == "" {
					return nil
				}
				sid, err := windows.StringToSid(value)
				if err != nil {
					t.Fatal(err)
				}
				return sid
			}
			if got := daemonPathOwnerAllowed(parse(tc.owner), parse(tc.user)); got != tc.want {
				t.Fatalf("owner admitted = %t, want %t", got, tc.want)
			}
		})
	}
}
