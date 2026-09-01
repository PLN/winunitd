//go:build windows

package pathwatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWindowsOpenWatchFiresOnDirModify(t *testing.T) {
	dir := t.TempDir()
	s, err := Parse(dir)
	if err != nil {
		t.Fatal(err)
	}
	w, err := OpenWatch(s)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.C():
	case <-time.After(3 * time.Second):
		t.Fatal("directory watch did not fire after write")
	}
}

func TestWindowsOpenWatchFileFilter(t *testing.T) {
	dir := t.TempDir()
	watched := filepath.Join(dir, "watched.txt")
	other := filepath.Join(dir, "other.txt")
	if err := os.WriteFile(watched, []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Parse(watched)
	if err != nil {
		t.Fatal(err)
	}
	w, err := OpenWatch(s)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if err := os.WriteFile(other, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.C():
		t.Fatal("sibling write must not fire a file-path watch")
	case <-time.After(400 * time.Millisecond):
	}

	if err := os.WriteFile(watched, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.C():
	case <-time.After(3 * time.Second):
		t.Fatal("file watch did not fire after write")
	}
}

func TestWindowsOpenWatchMissingPath(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "no-such-path")
	s, err := Parse(missing)
	if err != nil {
		t.Fatal(err)
	}
	w, err := OpenWatch(s)
	if err == nil || w != nil {
		if w != nil {
			_ = w.Close()
		}
		t.Fatal("missing path must fail")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Fatalf("err = %v", err)
	}
}

func TestWindowsExists(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope")
	s, err := ParseExists(missing)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := Exists(s)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("missing path must not exist")
	}
	if err := os.WriteFile(missing, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	ok, err = Exists(s)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("created path must exist")
	}
}

func TestWindowsOpenExistsWatchCreateToSatisfy(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "ready.flag")
	s, err := ParseExists(target)
	if err != nil {
		t.Fatal(err)
	}
	w, err := OpenExistsWatch(s)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if err := os.WriteFile(target, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.C():
	case <-time.After(3 * time.Second):
		t.Fatal("PathExists watch did not fire after create")
	}
}

func TestWindowsResolveExistsWatchMissingFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "ready.flag")
	watchDir, filter, err := resolveExistsWatch(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(watchDir, dir) {
		t.Fatalf("watchDir = %q, want %q", watchDir, dir)
	}
	if !strings.EqualFold(filter, "ready.flag") {
		t.Fatalf("filter = %q", filter)
	}
}

func TestWindowsResolveExistsWatchWalksUp(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sub", "ready.flag")
	watchDir, filter, err := resolveExistsWatch(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(watchDir, dir) {
		t.Fatalf("watchDir = %q, want %q", watchDir, dir)
	}
	if !strings.EqualFold(filter, "sub") {
		t.Fatalf("filter = %q, want sub", filter)
	}
}
