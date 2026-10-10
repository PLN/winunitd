//go:build windows

package headless

import (
	"runtime"
	"testing"

	"golang.org/x/sys/windows"
)

// A query's handles and callbacks stay on the thread that created the
// query, even when each callback yields and allocates. The Application log
// is readable without the Security log's privilege.
func TestEventQueryStaysOnItsThread(t *testing.T) {
	var threads []uint32
	err := channelEach("Application", "*", func(*auditEvent) (bool, error) {
		threads = append(threads, windows.GetCurrentThreadId())
		runtime.Gosched()
		runtime.GC()
		_ = make([]byte, 1<<16)
		return len(threads) < 100, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) == 0 {
		t.Skip("the Application log is empty")
	}
	for _, id := range threads {
		if id != threads[0] {
			t.Fatal("a query callback ran on another thread")
		}
	}
}
