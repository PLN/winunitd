package registry

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows"
)

func TestFailedOpenRetainsProtectedHandle(t *testing.T) {
	h, err := windows.CreateEvent(nil, 0, 0, nil)
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
	w := &winWatch{event: h, ch: make(chan struct{}), done: make(chan struct{})}
	failure := errors.New("initial notification arm failed")
	retained, err := failedOpenWatch(w, failure)
	if retained != w || !errors.Is(err, failure) || w.event != h {
		t.Fatal("failed open discarded remaining event")
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
	if w.event != 0 {
		t.Fatal("retry did not release event")
	}
	h = 0
}
