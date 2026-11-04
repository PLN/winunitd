//go:build windows

package journal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Journal files hold workload output, so their confidentiality must not rely
// on inherited directory ACLs: a manual installation may sit beneath a parent
// with inheritable read access for ordinary users. They follow the daemon-log
// convention instead. The store's process user is the scope owner (LocalSystem
// for the system manager, the signed-in user for a user manager, which runs in
// its own process). New files are created with that owner and a protected DACL
// allowing only the owner, SYSTEM and Administrators. Rotation renames within
// the journal directory, which keeps the descriptor on each archive.

// Match os.OpenFile(O_CREATE|O_RDWR|O_APPEND) access, plus WRITE_DAC so the
// handle itself can repair an existing file without a second path lookup.
const journalFileAccess = windows.GENERIC_READ | windows.FILE_APPEND_DATA | windows.FILE_WRITE_ATTRIBUTES |
	fileWriteEA | windows.READ_CONTROL | windows.WRITE_DAC | windows.SYNCHRONIZE

const fileWriteEA = 0x0010 // FILE_WRITE_EA

// journalFileSDDL fixes the owner explicitly. Windows rejects an owner that is
// absent from the creating token, so a thread that is accidentally
// impersonating another identity fails closed instead of creating a journal
// that identity owns and could re-open through its implicit WRITE_DAC.
func journalFileSDDL(sid *windows.SID) string {
	return "O:" + sid.String() + daemonPathSDDL(sid)
}

func journalProcessUser() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("journal process identity: %w", err)
	}
	return user.User.Sid, nil
}

// openJournalFile creates or opens a unit journal for appending. An existing
// file must be a single-link regular file with a trusted owner; its DACL is
// then replaced, so files from older versions or other creators are repaired.
func openJournalFile(path string) (*os.File, error) {
	user, err := journalProcessUser()
	if err != nil {
		return nil, err
	}
	sd, err := windows.SecurityDescriptorFromString(journalFileSDDL(user))
	if err != nil {
		return nil, err
	}
	sa := windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(sa))
	native, err := journalNativePath(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	h, err := windows.CreateFile(
		native,
		journalFileAccess,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		&sa,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	if err := protectJournalHandle(h, user, sd); err != nil {
		_ = windows.CloseHandle(h)
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

// protectJournalArchive applies the journal descriptor to an existing archive
// left by an older version. Archives rotated by this version already carry it.
func protectJournalArchive(path string) error {
	user, err := journalProcessUser()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString(journalFileSDDL(user))
	if err != nil {
		return err
	}
	native, err := journalNativePath(path)
	if err != nil {
		return &os.PathError{Op: "open", Path: path, Err: err}
	}
	h, err := windows.CreateFile(
		native,
		windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL|windows.WRITE_DAC,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
		return nil
	}
	if err != nil {
		return &os.PathError{Op: "open", Path: path, Err: err}
	}
	defer windows.CloseHandle(h)
	return protectJournalHandle(h, user, sd)
}

// protectJournalHandle works on the opened object, never on a re-resolved
// path. Reparse points and extra hard links are rejected before any security
// change so a planted name cannot relabel a file elsewhere.
func protectJournalHandle(h windows.Handle, user *windows.SID, sd *windows.SECURITY_DESCRIPTOR) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return fmt.Errorf("journal file information: %w", err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("journal file is a reparse point")
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return fmt.Errorf("journal file is a directory")
	}
	if info.NumberOfLinks != 1 {
		return fmt.Errorf("journal file has %d hard links", info.NumberOfLinks)
	}
	existing, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("journal file owner: %w", err)
	}
	owner, _, err := existing.Owner()
	if err != nil {
		return fmt.Errorf("journal file owner: %w", err)
	}
	if !daemonPathOwnerAllowed(owner, user) {
		return fmt.Errorf("journal file has unexpected owner")
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if err := windows.SetSecurityInfo(
		h,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	); err != nil {
		return fmt.Errorf("journal file DACL: %w", err)
	}
	return nil
}

// journalNativePath keeps long unit names usable, as os.OpenFile does, by
// using the extended-length form for long absolute paths.
func journalNativePath(path string) (*uint16, error) {
	if len(path) >= 248 && !strings.HasPrefix(path, `\\?\`) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		if rest, ok := strings.CutPrefix(abs, `\\`); ok {
			path = `\\?\UNC\` + rest
		} else {
			path = `\\?\` + abs
		}
	}
	return windows.UTF16PtrFromString(path)
}
