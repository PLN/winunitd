package pathwatch

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows"
)

func TestFailedOpenRetainsProtectedHandle(t *testing.T) {
	p, err := windows.UTF16PtrFromString(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(p, windows.FILE_LIST_DIRECTORY, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		t.Fatal(err)
	}
	const protect = 2
	t.Cleanup(func() {
		if h != 0 {
			_ = windows.SetHandleInformation(h, protect, 0)
			_ = windows.CloseHandle(h)
		}
	})
	if err := windows.SetHandleInformation(h, protect, protect); err != nil {
		t.Fatal(err)
	}
	w := &winWatch{dir: h, ch: make(chan struct{}), done: make(chan struct{})}
	failure := errors.New("event allocation failed")
	retained, err := failedOpenWatch(w, failure)
	if retained != w || !errors.Is(err, failure) || w.dir != h {
		t.Fatal("failed open discarded directory handle")
	}
	if _, ok := <-retained.C(); ok {
		t.Fatal("failed open channel remained active")
	}
	if err := windows.SetHandleInformation(h, protect, 0); err != nil {
		t.Fatal(err)
	}
	if err := retained.Close(); err != nil {
		t.Fatal(err)
	}
	if w.dir != 0 {
		t.Fatal("retry did not release directory")
	}
	h = 0
}
