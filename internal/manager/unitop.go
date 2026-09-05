package manager

import (
	"context"
	"sync"
)

// unitOps serializes start / stop / restart / auto-restart relaunch per
// unit name (issue #24). Independent units still start concurrently
// (DESIGN.md §69). Later ops wait and observe earlier results.
//
// Lock order: lock(name) before Manager.mu. Never acquire a unit op
// while holding Manager.mu.
type unitOps struct {
	mu sync.Mutex
	by map[string]chan struct{}
}

func (o *unitOps) semaphore(name string) chan struct{} {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.by == nil {
		o.by = make(map[string]chan struct{})
	}
	u := o.by[name]
	if u == nil {
		u = make(chan struct{}, 1)
		o.by[name] = u
	}
	return u
}

func (o *unitOps) lock(name string) func() {
	unlock, _ := o.lockContext(context.Background(), name)
	return unlock
}

// Cancellation abandons the wait, never the operation already holding the lock.
func (o *unitOps) lockContext(ctx context.Context, name string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if o == nil {
		return func() {}, nil
	}
	u := o.semaphore(name)
	select {
	case u <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-u
			return nil, err
		}
		return func() { <-u }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (o *unitOps) tryLock(name string) (func(), bool) {
	if o == nil {
		return func() {}, true
	}
	u := o.semaphore(name)
	select {
	case u <- struct{}{}:
		return func() { <-u }, true
	default:
		return nil, false
	}
}
