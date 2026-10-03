//go:build !windows

package servicing

import (
	"fmt"
	"os"
	"syscall"
)

// Other systems stand in for tests: the record and its directory must not be
// symbolic links or writable by group or others, and the record must belong
// to root or this process's user.
func openProtected(path string) (*os.File, error) {
	if err := protectedParent(parentDir(path)); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err == nil {
		err = protectedInfo(fi)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func protectedInfo(fi os.FileInfo) error {
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("floor record is not a regular file")
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("floor record is writable by other principals")
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Uid != 0 && int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("floor record has an untrusted owner")
	}
	return nil
}

func protectedParent(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("floor directory is not a directory")
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("floor directory is writable by other principals")
	}
	return nil
}

func createProtected(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
}
