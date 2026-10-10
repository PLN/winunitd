//go:build !windows

package headless

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

func runWindowsRole(role string, _ []string, _ io.Writer) error {
	return fmt.Errorf("%s requires Windows", role)
}

// securityFingerprint stands in for the security descriptor on other
// systems: owner, group and mode.
func securityFingerprint(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "", errors.New("no ownership information")
	}
	return fmt.Sprintf("%d:%d:%o", st.Uid, st.Gid, fi.Mode().Perm()), nil
}

func socketError(err error) uint32 {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return uint32(errno)
	}
	return 1
}
