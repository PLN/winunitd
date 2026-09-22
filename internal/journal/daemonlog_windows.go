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

func protectDaemonPath(path string, _ bool) error {
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;SY)(A;;FA;;;BA)")
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
