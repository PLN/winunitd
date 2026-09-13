//go:build windows

package journal

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsRotationLockedArchive(t *testing.T) {
	s := testStore(t)
	s.maxSize = 1
	const name = "rotation.service"
	for _, message := range []string{"one", "two", "three", "four"} {
		if err := s.append(Entry{Unit: name, Message: message}); err != nil {
			t.Fatal(err)
		}
		if err := s.syncUnit(name); err != nil {
			t.Fatal(err)
		}
	}
	path := s.path(name) + ".2"
	native, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(native, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if h != windows.InvalidHandle {
			_ = windows.CloseHandle(h)
		}
	}()
	before, err := os.ReadFile(s.path(name))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := s.append(Entry{Unit: name, Message: "five"}); err == nil {
			t.Fatal("locked archive rotation silently succeeded")
		}
		after, err := os.ReadFile(s.path(name))
		if err != nil || string(after) != string(before) {
			t.Fatal("failed rotation replaced current records", err)
		}
	}
	if err := windows.CloseHandle(h); err != nil {
		t.Fatal(err)
	}
	h = windows.InvalidHandle
	if err := s.append(Entry{Unit: name, Message: "five"}); err != nil {
		t.Fatal(err)
	}
	if err := s.syncUnit(name); err != nil {
		t.Fatal(err)
	}
	entries, err := s.Read(name)
	if err != nil || len(entries) != 4 {
		t.Fatalf("recovered history: %v, %v", entries, err)
	}
	for i, want := range []string{"two", "three", "four", "five"} {
		if entries[i].Message != want {
			t.Fatalf("recovered record %d=%q, want %q", i, entries[i].Message, want)
		}
	}
	assertDiskAccounting(t, s)
}
