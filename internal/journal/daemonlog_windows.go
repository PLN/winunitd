//go:build windows

package journal

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

func openAppend(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(
		p,
		windows.FILE_APPEND_DATA|windows.FILE_READ_DATA|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(h)
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("daemon log is a reparse point")
	}
	return os.NewFile(uintptr(h), path), nil
}

func finalPath(f *os.File) (string, error) {
	if f == nil {
		return "", fmt.Errorf("daemon log file required")
	}
	buf := make([]uint16, 32768)
	n, err := windows.GetFinalPathNameByHandle(windows.Handle(f.Fd()), &buf[0], uint32(len(buf)), 0)
	if err != nil {
		return "", err
	}
	if n == 0 || int(n) > len(buf) {
		return "", fmt.Errorf("daemon log path is unbounded")
	}
	return stripExtendedPath(windows.UTF16ToString(buf[:n])), nil
}

func pathWithin(root, path string) bool {
	return withinRoot(strings.ToLower(stripExtendedPath(root)), strings.ToLower(stripExtendedPath(path)))
}

func stripExtendedPath(path string) string {
	const unc = `\\?\UNC\`
	const ext = `\\?\`
	if strings.HasPrefix(path, unc) {
		return `\\` + path[len(unc):]
	}
	return strings.TrimPrefix(path, ext)
}

// daemonPathSDDL follows the user-control/notify principal set: the process
// user, SYSTEM and Administrators. User managers run in their own process, so
// use its token rather than a path owner or an RPC caller's impersonation token.
// LocalSystem keeps the original machine-only ACL without a duplicate ACE.
func daemonPathSDDL(sid *windows.SID) string {
	const machine = "D:P(A;;FA;;;SY)(A;;FA;;;BA)"
	if sid.IsWellKnown(windows.WinLocalSystemSid) {
		return machine
	}
	return machine + "(A;;FA;;;" + sid.String() + ")"
}

// daemonPathOwnerAllowed rejects objects that another untrusted principal could
// still rewrite through the owner's implicit WRITE_DAC, even after protection.
func daemonPathOwnerAllowed(owner, processUser *windows.SID) bool {
	return owner != nil && processUser != nil && (owner.Equals(processUser) ||
		owner.IsWellKnown(windows.WinLocalSystemSid) || owner.IsWellKnown(windows.WinBuiltinAdministratorsSid))
}

// The directory argument is needed by the Unix implementations to select mode
// 0700 or 0600. Windows grants the same full-control mask to files and directories.
func protectDaemonPath(path string, _ bool) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("daemon log process identity: %w", err)
	}
	existing, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("daemon log owner: %w", err)
	}
	owner, _, err := existing.Owner()
	if err != nil {
		return fmt.Errorf("daemon log owner: %w", err)
	}
	if !daemonPathOwnerAllowed(owner, user.User.Sid) {
		return fmt.Errorf("daemon log has unexpected owner")
	}
	sd, err := windows.SecurityDescriptorFromString(daemonPathSDDL(user.User.Sid))
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	)
}
