//go:build windows

package servicingtest

import (
	"testing"

	"golang.org/x/sys/windows"
)

// Protect replaces dir's DACL with a protected one that grants full control
// to SYSTEM, Administrators and the test's own account, inherited by new
// entries. The owner stays the test's account, which the store trusts for
// its own process.
func Protect(t testing.TB, dir string) {
	t.Helper()
	SetDACL(t, dir, ProtectedSDDL(t, ""))
}

// SetDACL replaces path's DACL with the protected DACL in sddl.
func SetDACL(t testing.TB, path, sddl string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
}

// ProtectedSDDL is Protect's DACL with extra ACEs appended.
func ProtectedSDDL(t testing.TB, extra string) string {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;" + user.User.Sid.String() + ")" + extra
}
