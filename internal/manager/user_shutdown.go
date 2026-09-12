package manager

import (
	"context"
	"errors"
	"sync"
)

const userShutdownWorkers = 4

// One retained pass owns this bounded worker group. A blocked SID gate does not
// delay every other user's stop, and retries join rather than duplicate workers.
func (h *UserHost) shutdownPass(ctx context.Context) error {
	sids := h.acceptUserShutdown()
	h.mu.Lock()
	dispatch := h.idleDispatch
	h.mu.Unlock()
	var mu sync.Mutex
	var result error
	record := func(err error) {
		mu.Lock()
		result = errors.Join(result, err)
		mu.Unlock()
	}
	var next int
	var workers sync.WaitGroup
	for i := 0; i < userShutdownWorkers && i < len(sids); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				mu.Lock()
				if next == len(sids) || ctx.Err() != nil {
					mu.Unlock()
					return
				}
				sid := sids[next]
				next++
				mu.Unlock()
				record(h.stopUser(ctx, sid, false))
			}
		}()
	}
	// Unknown-SID token work and close retries get their own progress path.
	record(h.drainNativeUserWork(ctx))
	workers.Wait()
	if dispatch != nil {
		select {
		case <-dispatch.done:
		case <-ctx.Done():
			record(ctx.Err())
		}
	}
	return errors.Join(result, ctx.Err())
}
