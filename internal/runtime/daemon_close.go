package runtime

import (
	"context"
	"sync"
)

type daemonCloseAttempt struct {
	done chan struct{}
	err  error
}

type daemonCloseWait struct {
	mu      sync.Mutex
	pending *daemonCloseAttempt
}

// CloseContext bounds the caller's wait for daemon-job closure. A call already
// inside the OS retains its handles; retries join it instead of starting another.
func (j *DaemonJob) CloseContext(ctx context.Context) error {
	if j == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	w := &j.closeWait
	w.mu.Lock()
	a := w.pending
	if a == nil {
		a = &daemonCloseAttempt{done: make(chan struct{})}
		w.pending = a
		go func() {
			a.err = j.Close()
			w.mu.Lock()
			w.pending = nil
			w.mu.Unlock()
			close(a.done)
		}()
	}
	w.mu.Unlock()
	select {
	case <-a.done:
		return a.err
	case <-ctx.Done():
		return ctx.Err()
	}
}
