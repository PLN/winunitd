package manager

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func admissionFileTrusted(f *os.File) error {
	sd, err := windows.GetSecurityInfo(windows.Handle(f.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	return admissionSecurityTrusted(sd)
}

func admissionSecurityTrusted(sd *windows.SECURITY_DESCRIPTOR) error {
	trusted := func(sid *windows.SID) bool {
		return sid != nil && (sid.IsWellKnown(windows.WinLocalSystemSid) || sid.IsWellKnown(windows.WinBuiltinAdministratorsSid))
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	if !trusted(owner) {
		return fmt.Errorf("user admission policy must be owned by SYSTEM or Administrators")
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if acl == nil {
		return fmt.Errorf("user admission policy requires a restrictive DACL")
	}
	const mutating = windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_EA | windows.FILE_WRITE_ATTRIBUTES | windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER | windows.GENERIC_WRITE | windows.GENERIC_ALL
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			return err
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 || ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("unsupported user admission policy ACL")
		}
		if uint32(ace.Mask)&uint32(mutating) != 0 && !trusted((*windows.SID)(unsafe.Pointer(&ace.SidStart))) {
			return fmt.Errorf("user admission policy permits non-administrator modification")
		}
	}
	return nil
}
