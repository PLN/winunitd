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
