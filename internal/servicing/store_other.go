//go:build !windows

package servicing

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"
)

// Other systems stand in for tests with the same rules expressed in modes:
// no level is a symbolic link; the containing directory, when group or
// others may write it, is sticky so they cannot remove what they do not
// own; the data root, daemon directory and record are not writable by group
// or others; each belongs to root or this process's user.

type noDirs struct{}

func (noDirs) Close() error { return nil }

func openFloorDirs(path string) (io.Closer, bool, error) {
	container, root, daemon := floorChain(path)
	fi, err := os.Lstat(container)
	if err != nil {
		return nil, false, fmt.Errorf("floor data root's containing directory: %w", err)
	}
	if err := trustedDir(fi, "containing directory"); err != nil {
		return nil, false, err
	}
	if fi.Mode().Perm()&0o022 != 0 && fi.Mode()&os.ModeSticky == 0 {
		return nil, false, errors.New("floor data root's containing directory lets other principals remove entries")
	}
	for _, level := range []struct{ path, name string }{{root, "data root"}, {daemon, "daemon directory"}} {
		fi, err := os.Lstat(level.path)
		if errors.Is(err, os.ErrNotExist) {
			return noDirs{}, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		if err := trustedDir(fi, level.name); err != nil {
			return nil, false, err
		}
		if fi.Mode().Perm()&0o022 != 0 {
			return nil, false, fmt.Errorf("floor %s is writable by other principals", level.name)
		}
	}
	return noDirs{}, true, nil
}

func trustedDir(fi os.FileInfo, name string) error {
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("floor %s is a symbolic link", name)
	}
	if !fi.IsDir() {
		return fmt.Errorf("floor %s is not a directory", name)
	}
	if !trustedOwner(fi) {
		return fmt.Errorf("floor %s has an untrusted owner", name)
	}
	return nil
}

func trustedOwner(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return !ok || st.Uid == 0 || int(st.Uid) == os.Geteuid()
}

// openProtected opens the record itself, never a link target.
func openProtected(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, errors.New("floor record is a symbolic link")
		}
		return nil, err
	}
	fi, err := f.Stat()
	if err == nil {
		err = protectedInfo(fi, "floor record")
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func protectedInfo(fi os.FileInfo, name string) error {
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", name)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is writable by other principals", name)
	}
	if !trustedOwner(fi) {
		return fmt.Errorf("%s has an untrusted owner", name)
	}
	return nil
}

// lockFloor opens or creates the lock file itself, checks it like the
// record and takes an exclusive flock on it. Closing the file releases it.
func lockFloor(path string, wait time.Duration) (io.Closer, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, errors.New("floor lock is a symbolic link")
		}
		return nil, fmt.Errorf("floor lock: %w", err)
	}
	fi, err := f.Stat()
	if err == nil {
		err = protectedInfo(fi, "floor lock")
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			_ = f.Close()
			return nil, fmt.Errorf("floor lock: %w", err)
		}
		if !time.Now().Before(deadline) {
			_ = f.Close()
			return nil, ErrFloorBusy
		}
		time.Sleep(lockPoll)
	}
}

func createProtected(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
}
