// Package servicingtest prepares compatibility-floor directories for tests:
// fresh directories protected the way the product's data root is, so the
// floor store trusts them on every system.
package servicingtest

import (
	"os"
	"path/filepath"
	"testing"
)

// Root returns a fresh protected directory: a data root without its daemon
// directory, or a safe container for one.
func Root(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	Protect(t, dir)
	return dir
}

// DataRoot returns a fresh protected data root with its daemon directory.
func DataRoot(t testing.TB) string {
	t.Helper()
	base := Root(t)
	daemon := filepath.Join(base, "daemon")
	if err := os.Mkdir(daemon, 0o755); err != nil {
		t.Fatal(err)
	}
	Protect(t, daemon)
	return base
}
