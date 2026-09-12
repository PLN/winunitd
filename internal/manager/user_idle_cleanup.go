package manager

import (
	"context"
	"time"
)

const userIdleCleanupWorkers = 4

type userIdleRequest struct {
	revision uint64
	active   bool
	due      time.Time
}

type userIdleDispatch struct {
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	wake    chan struct{}
	pending map[string]*userIdleRequest // guarded by h.mu; only retained SIDs
	active  int
}

// The notification listener records logoff before requesting cleanup. One
// retained dispatcher owns a bounded worker group; busy SID gates consume no
// workers, and repeated notifications coalesce into the existing SID entry.
func (h *UserHost) queueIdleCleanup(sid string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || sid == "" || h.bySID[sid] == nil {
		return
	}
	d := h.idleDispatch
	if d == nil {
		ctx, cancel := context.WithCancel(context.Background())
		d = &userIdleDispatch{ctx: ctx, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1), pending: make(map[string]*userIdleRequest)}
		h.idleDispatch = d
		go h.runIdleCleanup(d)
	}
	r := d.pending[sid]
	if r == nil {
		r = &userIdleRequest{}
		d.pending[sid] = r
	}
	r.revision++
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (h *UserHost) runIdleCleanup(d *userIdleDispatch) {
	defer close(d.done)
	defer d.cancel()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		h.mu.Lock()
		if d.active == 0 && (h.closed || len(d.pending) == 0) {
			if h.idleDispatch == d {
				h.idleDispatch = nil
			}
			h.mu.Unlock()
			return
		}
		var candidates []string
		if !h.closed && d.active < userIdleCleanupWorkers {
			for sid, r := range d.pending {
				if !r.active && !time.Now().Before(r.due) {
					candidates = append(candidates, sid)
				}
			}
		}
		h.mu.Unlock()
		for _, sid := range candidates {
			unlock, ok := h.ops.tryLock(sid)
			if !ok {
				continue
			}
			h.mu.Lock()
			r := d.pending[sid]
			if h.closed || d.active == userIdleCleanupWorkers || r == nil || r.active {
				h.mu.Unlock()
				unlock()
				continue
			}
			r.active = true
			revision := r.revision
			d.active++
			h.mu.Unlock()
			go func(sid string, r *userIdleRequest, revision uint64, unlock func()) {
				// The native stop remains owned by stopSet and bySID if this
				// worker's wait expires. Independent users then get capacity.
				ctx, cancel := context.WithTimeout(d.ctx, time.Second)
				err := h.stopUserLocked(ctx, sid, true)
				cancel()
				unlock()
				h.mu.Lock()
				d.active--
				r.active = false
				if (err == nil && r.revision == revision) || h.bySID[sid] == nil {
					delete(d.pending, sid)
				} else {
					r.due = time.Now().Add(time.Second)
				}
				h.mu.Unlock()
				select {
				case d.wake <- struct{}{}:
				default:
				}
			}(sid, r, revision, unlock)
		}
		select {
		case <-d.wake:
		case <-tick.C:
		}
	}
}
