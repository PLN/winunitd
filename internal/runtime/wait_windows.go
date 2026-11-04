//go:build windows

package runtime

import (
	"context"
	"fmt"
	goruntime "runtime"

	"golang.org/x/sys/windows"
)

// waitProcess waits for owned handle h. It is WaitForSingleObject on the
// process plus the H3 cancel-event pattern (issue #30): a manual-reset
// event and WaitForMultipleObjects so ctx.Done() does not CloseHandle a
// handle another thread is waiting on. The wait goroutine owns h and the
// duplicated cancel handle. Callers must DuplicateHandle first so Close
// of the original process handle is safe. record, if non-nil, is called
// with the exit code when the process exits.
func waitProcess(ctx context.Context, h windows.Handle, record func(uint32)) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if h == 0 {
		return fmt.Errorf("process handle is closed")
	}

	cancelEvent, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		_ = windows.CloseHandle(h)
		return err
	}
	var cancelWait windows.Handle
	err = windows.DuplicateHandle(
		windows.CurrentProcess(),
		cancelEvent,
		windows.CurrentProcess(),
		&cancelWait,
		0,
		false,
		windows.DUPLICATE_SAME_ACCESS,
	)
	if err != nil {
		_ = windows.CloseHandle(h)
		_ = windows.CloseHandle(cancelEvent)
		return err
	}
	defer windows.CloseHandle(cancelEvent)

	done := make(chan error, 1)
	go func() {
		defer func() {
			_ = windows.CloseHandle(h)
			_ = windows.CloseHandle(cancelWait)
		}()
		handles := []windows.Handle{h, cancelWait}
		s, err := windows.WaitForMultipleObjects(handles, false, windows.INFINITE)
		goruntime.KeepAlive(handles)
		if err != nil {
			done <- err
			return
		}
		switch s {
		case windows.WAIT_OBJECT_0:
			// A signalled process handle means the process has exited, and
			// its exit code is final before the handle signals. The code can
			// legitimately be STILL_ACTIVE (259): it is an ordinary exit code
			// here, never a reason to wait again. The signalled handle stays
			// signalled and would win every later wait, including over the
			// cancel event.
			var exit uint32
			if err := windows.GetExitCodeProcess(h, &exit); err != nil {
				done <- err
				return
			}
			if record != nil {
				record(exit)
			}
			if exit == 0 {
				done <- nil
				return
			}
			done <- &ExitStatus{Code: exit}
		case windows.WAIT_OBJECT_0 + 1:
			done <- errWaitCanceled
		default:
			done <- fmt.Errorf("WaitForMultipleObjects: %d", s)
		}
	}()

	select {
	case err := <-done:
		if err == errWaitCanceled {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		return err
	case <-ctx.Done():
		_ = windows.SetEvent(cancelEvent)
		<-done
		return ctx.Err()
	}
}

// processHandleExited reports whether process handle h has signalled, which
// is the only reliable exit test: GetExitCodeProcess returns STILL_ACTIVE
// (259) both for a running process and for one that exited with code 259.
// h needs SYNCHRONIZE access.
func processHandleExited(h windows.Handle) (bool, error) {
	s, err := windows.WaitForSingleObject(h, 0)
	if err != nil {
		return false, err
	}
	switch s {
	case windows.WAIT_OBJECT_0:
		return true, nil
	case uint32(windows.WAIT_TIMEOUT):
		return false, nil
	default:
		return false, fmt.Errorf("WaitForSingleObject: %d", s)
	}
}
