//go:build windows

package winio

import (
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// Force close-channel selection before delivering the raced connection result.
// No OS scheduling coincidence or globally mutable test hook is needed.
func TestConsumedCloseOverridesConnectionResult(t *testing.T) {
	for _, result := range []error{nil, ErrFileClosed, windows.ERROR_NO_DATA, windows.ERROR_OPERATION_ABORTED, windows.ERROR_ACCESS_DENIED} {
		l := &win32PipeListener{closeCh: make(chan int)}
		completion := make(chan error, 1)
		done := make(chan error, 1)
		go func() {
			// No native resource is owned by this fixture.
			p, err := l.waitForPipeConnection(&win32File{handle: windows.InvalidHandle}, completion)
			if p != nil {
				t.Error("closed connection retained a pipe")
			}
			done <- err
		}()
		select {
		case l.closeCh <- 1:
		case <-time.After(5 * time.Second):
			t.Fatal("close request was not consumed")
		}
		completion <- result
		select {
		case err := <-done:
			if err != ErrPipeListenerClosed {
				t.Fatalf("connection result %v overrode consumed close: %v", result, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("close did not finish after connection completion")
		}
	}
}

func TestConnectionErrorWithoutClosePreserved(t *testing.T) {
	l := &win32PipeListener{closeCh: make(chan int)}
	completion := make(chan error, 1)
	completion <- windows.ERROR_ACCESS_DENIED
	p, err := l.waitForPipeConnection(&win32File{handle: windows.InvalidHandle}, completion)
	if p != nil || err != windows.ERROR_ACCESS_DENIED {
		t.Fatal("ordinary connection failure changed")
	}
}
