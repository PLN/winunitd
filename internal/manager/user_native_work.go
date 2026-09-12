package manager

import (
	"context"
	"errors"
	"fmt"

	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/timers"
)

// Failed token cleanup retains its slot. There can never be more pending native
// logon/linger operations than this limit, including work before the SID is known.
const maxNativeUserWork = 4

type userNativeWork struct {
	done chan struct{}
	// token is set under h.mu only after the worker's first close fails.
	token *runtime.UserToken
}

func (h *UserHost) acceptNativeUserWork() (*userNativeWork, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, fmt.Errorf("user host is shutting down or closed")
	}
	if len(h.nativeWork) >= maxNativeUserWork {
		return nil, protocol.ErrBusy()
	}
	if h.nativeWork == nil {
		h.nativeWork = make(map[*userNativeWork]struct{})
	}
	w := &userNativeWork{done: make(chan struct{})}
	h.nativeWork[w] = struct{}{}
	return w, nil
}

func (h *UserHost) finishNativeUserWork(w *userNativeWork, token *runtime.UserToken) error {
	var err error
	if token != nil {
		err = h.cfg.CloseToken(token)
	}
	h.mu.Lock()
	if err == nil {
		delete(h.nativeWork, w)
	} else {
		w.token = token
	}
	close(w.done)
	h.mu.Unlock()
	return err
}

// Admission is closed before this snapshot. Unknown-SID lookups cannot escape
// shutdown merely because they have not published a user-manager instance yet.
func (h *UserHost) drainNativeUserWork(ctx context.Context) error {
	return h.cleanupNativeUserWork(ctx, true)
}

// Reconciliation retries completed failures without waiting for live lookups.
// Both paths join the same close attempt, including after a caller times out.
func (h *UserHost) cleanupNativeUserWork(ctx context.Context, drain bool) error {
	h.mu.Lock()
	work := make([]*userNativeWork, 0, len(h.nativeWork))
	for w := range h.nativeWork {
		work = append(work, w)
	}
	h.mu.Unlock()
	var result error
	for _, w := range work {
		if !drain {
			select {
			case <-w.done:
			default:
				continue
			}
		}
		select {
		case <-w.done:
		case <-ctx.Done():
			return errors.Join(result, ctx.Err())
		}
		err := h.stops.wait(ctx, timers.DefaultClock(), stopKey{userWork: w}, 0, func() error {
			h.mu.Lock()
			_, retained := h.nativeWork[w]
			token := w.token
			h.mu.Unlock()
			if !retained || token == nil {
				return nil
			}
			if err := h.cfg.CloseToken(token); err != nil {
				return fmt.Errorf("retained user token cleanup: %w", err)
			}
			h.mu.Lock()
			delete(h.nativeWork, w)
			h.mu.Unlock()
			return nil
		})
		result = errors.Join(result, err)
	}
	return result
}

// NativeWorkCount includes active lookups/launches and retained token cleanup.
func (h *UserHost) NativeWorkCount() int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.nativeWork)
}
