//go:build windows

package journal

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsDiskRetentionLockedHistory(t *testing.T) {
	s := testStore(t)
	s.maxDiskFiles = 1
	writeRetiredJournal(t, s, "locked.service", "retained")
	path, err := windows.UTF16PtrFromString(s.path("locked.service"))
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(path, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if h != windows.InvalidHandle {
			_ = windows.CloseHandle(h)
		}
	}()
	for i := 0; i < 3; i++ {
		if err := s.append(Entry{Unit: "next.service", Message: "rejected"}); !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			t.Fatal("expected native sharing violation", err)
		}
		if _, err := os.Stat(s.path("locked.service")); err != nil {
			t.Fatal("locked history disappeared", err)
		}
		if s.TotalCaptureStats().EvictedFiles != 0 {
			t.Fatal("failed deletion counted as eviction")
		}
	}
	if err := windows.CloseHandle(h); err != nil {
		t.Fatal(err)
	}
	h = windows.InvalidHandle
	writeRetiredJournal(t, s, "next.service", "after handle release")
	if s.TotalCaptureStats().EvictedFiles != 1 {
		t.Fatal("recovered deletion not recorded")
	}
	assertDiskAccounting(t, s)
}
