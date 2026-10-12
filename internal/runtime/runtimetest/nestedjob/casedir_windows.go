//go:build windows

package nestedjob

import (
	"golang.org/x/sys/windows"
)

// ProtectDirectory replaces dir's DACL so that only SYSTEM, Administrators
// and, when sid is set, that account have access, inherited by new files.
// Headless cases use it so the SYSTEM observer and the account's processes
// share a case directory that no other principal can change.
func ProtectDirectory(dir, sid string) error {
	sddl := "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	if sid != "" {
		sddl += "(A;OICI;FA;;;" + sid + ")"
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}
