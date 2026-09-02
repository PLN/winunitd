package manager

import "sync"

// unitOps serializes start / stop / restart / auto-restart relaunch per
// unit name (issue #24). Independent units still start concurrently
// (DESIGN.md §69). Later ops wait and observe earlier results.
//
// Lock order: lock(name) before Manager.mu. Never acquire a unit op
// while holding Manager.mu.
type unitOps struct {
	mu sync.Mutex
	by map[string]*sync.Mutex
}

func (o *unitOps) lock(name string) func() {
	if o == nil {
		return func() {}
	}
	o.mu.Lock()
	if o.by == nil {
		o.by = make(map[string]*sync.Mutex)
	}
	u := o.by[name]
	if u == nil {
		u = new(sync.Mutex)
		o.by[name] = u
	}
	o.mu.Unlock()
	u.Lock()
	return u.Unlock
}

func (o *unitOps) tryLock(name string) (func(), bool) {
	if o == nil {
		return func() {}, true
	}
	o.mu.Lock()
	if o.by == nil {
		o.by = make(map[string]*sync.Mutex)
	}
	u := o.by[name]
	if u == nil {
		u = new(sync.Mutex)
		o.by[name] = u
	}
	o.mu.Unlock()
	if !u.TryLock() {
		return nil, false
	}
	return u.Unlock, true
}
