//go:build !windows

package journal

import (
	"os"
	"testing"
)

func assertProtectedDaemonDir(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("daemon directory mode = %v err=%v", info, err)
	}
}
