package timers

// A single reserved loader drains pending arms. Disarm/rearm can invalidate an
// observation while storage is blocked, without creating additional workers.
func (e *Engine) loadPending() {
	defer e.storageWork.Done()
	for {
		e.mu.Lock()
		var next *armed
		if e.running {
			for _, a := range e.armed {
				if a.loading {
					next = a
					break
				}
			}
		}
		if next == nil {
			e.loading = false
			e.mu.Unlock()
			return
		}
		name := next.spec.Name
		e.mu.Unlock()
		e.storageIO.Lock()
		e.mu.Lock()
		current := e.running && e.armed[name] == next && next.loading
		e.mu.Unlock()
		if !current {
			e.storageIO.Unlock()
			continue
		}
		rt, err := e.store.LoadChecked(name)
		e.mu.Lock()
		if e.running && e.armed[name] == next && next.loading {
			next.loading = false
			if err != nil {
				next.storageError = err.Error()
			} else {
				rt.LastUnitActive = next.rt.LastUnitActive
				next.rt = rt
				next.storageError = ""
			}
			e.rescheduleLocked(next)
			e.kickLocked()
		}
		e.mu.Unlock()
		e.storageIO.Unlock()
	}
}

// The callback slot or explicit result operation owns this wait. Serializing
// reads and writes prevents a stale write from overtaking a replacement load.
// Decision locks remain free throughout the filesystem call.
func (e *Engine) persist(event Fire, rt Runtime) bool {
	e.storageIO.Lock()
	defer e.storageIO.Unlock()
	e.mu.Lock()
	a := e.armed[event.Name]
	current := e.running && a != nil && a.token == event.Token && a.storageError == ""
	e.mu.Unlock()
	if !current {
		return false
	}
	err := e.store.Save(event.Name, rt)
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running || e.armed[event.Name] != a || a.token != event.Token {
		return false
	}
	if err != nil {
		a.storageError = err.Error()
		e.rescheduleLocked(a)
		e.kickLocked()
		return false
	}
	return true
}
