package manager

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/timers"
)

type stopKey struct {
	manager    *Manager
	proc       runtime.Process
	user       runtime.UserManagerProc
	nativeKind string
	nativeName string
}

type stopAttempt struct {
	done chan struct{}
	err  error
}

type stopSet struct {
	mu      sync.Mutex
	pending map[stopKey]*stopAttempt
}

// wait bounds the caller's wait while retaining a single pending adapter
// operation per process or native target, including calls blocked inside the OS.
func (s *stopSet) wait(ctx context.Context, clk timers.Clock, key stopKey, timeout time.Duration, stop func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// A caller deadline does not cancel an adapter call already inside the OS.
	// Keep one attempt per target; retries join it instead of closing handles
	// concurrently or accumulating another blocked Stop goroutine each time.
	s.mu.Lock()
	if s.pending == nil {
		s.pending = make(map[stopKey]*stopAttempt)
	}
	attempt := s.pending[key]
	if attempt == nil {
		attempt = &stopAttempt{done: make(chan struct{})}
		s.pending[key] = attempt
		go func(a *stopAttempt) {
			a.err = stop()
			s.mu.Lock()
			delete(s.pending, key)
			s.mu.Unlock()
			close(a.done)
		}(attempt)
	}
	s.mu.Unlock()
	if timeout <= 0 {
		select {
		case <-attempt.done:
			return attempt.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	t := clk.Timer(timeout)
	defer t.Stop()
	select {
	case <-attempt.done:
		return attempt.err
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C():
		return fmt.Errorf("TimeoutStopSec exceeded")
	}
}
