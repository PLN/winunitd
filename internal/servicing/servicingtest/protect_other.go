//go:build !windows

package servicingtest

import (
	"os"
	"testing"
)

// Protect makes dir writable by its owner only.
func Protect(t testing.TB, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

// LetOthersWrite makes path writable by group and others.
func LetOthersWrite(t testing.TB, path string) {
	t.Helper()
	if err := os.Chmod(path, 0o777); err != nil {
		t.Fatal(err)
	}
}
