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
	by map[string]*unitOp
}

type unitOp struct {
	gate chan struct{}
	refs int // holder plus every accepted waiter, protected by unitOps.mu
}

// Pin the entry before waiting, so reclamation cannot split one unit across
// two independent gates while an old holder or waiter still references it.
func (o *unitOps) retain(name string) *unitOp {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.by == nil {
		o.by = make(map[string]*unitOp)
	}
	u := o.by[name]
	if u == nil {
		u = &unitOp{gate: make(chan struct{}, 1)}
		o.by[name] = u
	}
	u.refs++
	return u
}

func (o *unitOps) release(name string, u *unitOp) {
	o.mu.Lock()
	defer o.mu.Unlock()
	u.refs--
	if u.refs == 0 {
		delete(o.by, name)
	}
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
	u := o.retain(name)
	select {
	case u.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-u.gate
			o.release(name, u)
			return nil, err
		}
		return func() { <-u.gate; o.release(name, u) }, nil
	case <-ctx.Done():
		o.release(name, u)
		return nil, ctx.Err()
	}
}

func (o *unitOps) tryLock(name string) (func(), bool) {
	if o == nil {
		return func() {}, true
	}
	u := o.retain(name)
	select {
	case u.gate <- struct{}{}:
		return func() { <-u.gate; o.release(name, u) }, true
	default:
		o.release(name, u)
		return nil, false
	}
}
