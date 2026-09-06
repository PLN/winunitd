package manager

import "time"

// runtimeIdentity belongs to one runtime record and generation. A name and
// generation alone can be reused after removal/recreation of a unit.
type runtimeIdentity struct {
	name   string
	record *unitRuntime
	gen    uint64
}

// Call with m.mu held. Payloads remain immutable after capture.
func (id runtimeIdentity) currentLocked(m *Manager) bool {
	return id.record != nil && m.units[id.name] == id.record && id.record.gen == id.gen
}

type recoveryRequest struct {
	owner runtimeIdentity
	delay time.Duration
}
