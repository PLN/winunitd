//go:build windows

package servicing

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ACE types beyond the two x/sys names. A callback ACE carries the same
// header, mask and SID as its plain form, followed by a condition.
const (
	accessAllowedCallbackACE = 0x9
	accessDeniedCallbackACE  = 0xA
)

const (
	fileDeleteChild = 0x0040
	// writeRights would let a principal change, delete or re-permission a
	// protected directory, its entries or the record.
	writeRights = windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_EA | windows.FILE_WRITE_ATTRIBUTES |
		fileDeleteChild | windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER | windows.GENERIC_WRITE | windows.GENERIC_ALL
	// replaceRights would let a principal delete or rename an entry of the
	// containing directory, or take control of it. Creating new entries
	// there, which ProgramData allows its users, is not among them.
	replaceRights = fileDeleteChild | windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER | windows.GENERIC_ALL
)

// trustedInstaller is NT SERVICE\TrustedInstaller.
const trustedInstaller = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"

// principals is the calling process's account and whether its token is
// elevated. The manager and the package helper run as SYSTEM.
func principals() (*windows.SID, bool, error) {
	tok := windows.GetCurrentProcessToken()
	user, err := tok.GetTokenUser()
	if err != nil {
		return nil, false, fmt.Errorf("floor caller identity: %w", err)
	}
	return user.User.Sid, tok.IsElevated(), nil
}

func trustedSID(sid, self *windows.SID) bool {
	return sid != nil && (sid.IsWellKnown(windows.WinLocalSystemSid) || sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) ||
		sid.String() == trustedInstaller || (self != nil && sid.Equals(self)))
}

// heldDirs keeps the checked chain open. The handles deny delete sharing, so
// no level can be renamed or replaced while an operation uses it.
type heldDirs []windows.Handle

func (h heldDirs) Close() error {
	var errs []error
	for _, handle := range h {
		errs = append(errs, windows.CloseHandle(handle))
	}
	return errors.Join(errs...)
}

func openFloorDirs(path string) (io.Closer, bool, error) {
	self, _, err := principals()
	if err != nil {
		return nil, false, err
	}
	container, root, daemon := floorChain(path)
	var held heldDirs
	fail := func(err error) (io.Closer, bool, error) {
		_ = held.Close()
		return nil, false, err
	}
	h, err := openDir(container)
	if err != nil {
		return fail(fmt.Errorf("floor data root's containing directory: %w", err))
	}
	held = append(held, h)
	if err := checkDir(h, "data root's containing directory", replaceRights, false, self); err != nil {
		return fail(err)
	}
	for _, level := range []struct{ path, name string }{{root, "data root"}, {daemon, "daemon directory"}} {
		h, err := openDir(level.path)
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return held, false, nil
		}
		if err != nil {
			return fail(fmt.Errorf("floor %s: %w", level.name, err))
		}
		held = append(held, h)
		if err := checkDir(h, level.name, writeRights, true, self); err != nil {
			return fail(err)
		}
	}
	return held, true, nil
}

// openDir opens a directory itself, never a reparse target, for its
// attributes and security only, sharing read and write but not delete.
func openDir(path string) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		h, err := windows.CreateFile(p, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
			nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if err == nil {
			return h, nil
		}
		if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) || time.Now().After(deadline) {
			return 0, err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// checkDir checks one level on its handle: a directory, not a reparse point,
// a trusted owner and no forbidden right for anyone else. Inherit-only
// grants count where they shape the record and its siblings.
func checkDir(h windows.Handle, name string, forbidden uint32, inheritOnly bool, self *windows.SID) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("floor %s is a reparse point", name)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return fmt.Errorf("floor %s is not a directory", name)
	}
	return checkSecurity(h, "floor "+name, forbidden, inheritOnly, self)
}

func checkSecurity(h windows.Handle, name string, forbidden uint32, inheritOnly bool, self *windows.SID) error {
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil || !trustedSID(owner, self) {
		return fmt.Errorf("%s must be owned by SYSTEM or Administrators", name)
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		return fmt.Errorf("%s requires a restrictive DACL", name)
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			return err
		}
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE, accessDeniedCallbackACE:
			continue
		case windows.ACCESS_ALLOWED_ACE_TYPE, accessAllowedCallbackACE:
		default:
			return fmt.Errorf("%s has an unsupported ACL", name)
		}
		if !inheritOnly && ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		if uint32(ace.Mask)&forbidden != 0 && !trustedSID((*windows.SID)(unsafe.Pointer(&ace.SidStart)), self) {
			return fmt.Errorf("%s permits non-administrator writes", name)
		}
	}
	return nil
}

// openProtected opens the record itself, never a reparse target, and checks
// its owner and DACL on the open handle.
func openProtected(path string) (*os.File, error) {
	self, _, err := principals()
	if err != nil {
		return nil, err
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.READ_CONTROL, windows.FILE_SHARE_READ, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	if err := protectedHandle(h, self); err != nil {
		_ = windows.CloseHandle(h)
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

func protectedHandle(h windows.Handle, self *windows.SID) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("floor record is a reparse point")
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return fmt.Errorf("floor record is not a regular file")
	}
	return checkSecurity(h, "floor record", writeRights, true, self)
}

// floorSDDL grants full control to SYSTEM and Administrators only, without
// inheritance; the owner is the writer's default owner, Administrators for
// an elevated administrator. A non-elevated writer, which can only be a test
// in its own directory, also keeps access for itself.
func floorSDDL() (string, error) {
	self, elevated, err := principals()
	if err != nil {
		return "", err
	}
	const machine = "D:P(A;;FA;;;SY)(A;;FA;;;BA)"
	if elevated || self.IsWellKnown(windows.WinLocalSystemSid) {
		return machine, nil
	}
	return machine + "(A;;FA;;;" + self.String() + ")", nil
}

func createProtected(path string) (*os.File, error) {
	sddl, err := floorSDDL()
	if err != nil {
		return nil, err
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_WRITE, 0, &sa, windows.CREATE_NEW,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "create", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}
