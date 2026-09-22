//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectDirectoryRejectsUserOwner(t *testing.T) {
	fact, err := inspectDirectory("units", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	err = classifyDirectory(fact)
	// A directory created here is not a product directory. An elevated
	// token owns it as Administrators, so preflight reports the write
	// grant. A standard user token is rejected for ownership. Either
	// result stays fail-closed and keeps the conflict marker.
	if !userDirectoryConflict(err) {
		t.Fatalf("user-owned directory: %v", err)
	}
}

func userDirectoryConflict(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	if !strings.Contains(msg, "preflight conflict:") {
		return false
	}
	return strings.Contains(msg, "unexpected ownership") || strings.Contains(msg, "permits non-administrator writes")
}

func TestInspectDirectoryRejectsReparseWithoutOpeningTarget(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(target, "secret")
	if err := os.WriteFile(secret, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	fact, err := inspectDirectory("units", link)
	if err != nil {
		t.Fatal(err)
	}
	if !fact.Reparse {
		t.Fatal("reparse point was not detected")
	}
	err = classifyDirectory(fact)
	if err == nil || !strings.Contains(err.Error(), "reparse point") {
		t.Fatalf("reparse classification: %v", err)
	}
	got, err := os.ReadFile(secret)
	if err != nil || string(got) != "keep" {
		t.Fatalf("target bytes = %q %v", got, err)
	}
}
