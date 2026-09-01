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
		for {
			s, err := windows.WaitForMultipleObjects(handles, false, windows.INFINITE)
			goruntime.KeepAlive(handles)
			if err != nil {
				done <- err
				return
			}
			switch s {
			case windows.WAIT_OBJECT_0:
				var exit uint32
				if err := windows.GetExitCodeProcess(h, &exit); err != nil {
					done <- err
					return
				}
				if exit == stillActiveExit {
					continue
				}
				if record != nil {
					record(exit)
				}
				if exit == 0 {
					done <- nil
					return
				}
				done <- &ExitStatus{Code: exit}
				return
			case windows.WAIT_OBJECT_0 + 1:
				done <- errWaitCanceled
				return
			default:
				done <- fmt.Errorf("WaitForMultipleObjects: %d", s)
				return
			}
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
