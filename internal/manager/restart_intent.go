package manager

// restartOrigin prevents a pending explicit restart from undoing a later stop.
// It also protects queued dependency launches after the start phase is admitted.
type restartOrigin struct {
	name      string
	record    *unitRuntime
	stopEpoch uint64
}

func (o *restartOrigin) validLocked(m *Manager) bool {
	rt := m.units[o.name]
	return !m.closed && rt == o.record && rt != nil && rt.stopEpoch == o.stopEpoch && !rt.unavailable
}

// Explicit restarts retain explicit-start policy; they are not trigger retries.
func (*restartOrigin) countsStartLimit() bool { return false }
