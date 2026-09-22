//go:build linux

package journal

import (
	"fmt"
	"os"
	"syscall"
)

func openAppend(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
}

func finalPath(f *os.File) (string, error) {
	if f == nil {
		return "", fmt.Errorf("daemon log file required")
	}
	return os.Readlink(fmt.Sprintf("/proc/self/fd/%d", f.Fd()))
}

func pathWithin(root, path string) bool {
	return withinRoot(root, path)
}

func protectDaemonPath(path string, directory bool) error {
	mode := os.FileMode(0o600)
	if directory {
		mode = 0o700
	}
	return os.Chmod(path, mode)
}
