package manager

import "github.com/PLN/winunitd/internal/protocol"

// Caller owns configMu. Register before I/O so close can join this work without
// holding lifecycle locks or accumulating more native waiters on retries.
func (m *Manager) beginConfigWork() (func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, protocol.ErrFailed("manager is shutting down or closed")
	}
	done := make(chan struct{})
	m.configWork = done
	return func() {
		m.mu.Lock()
		m.configWork = nil
		close(done)
		m.mu.Unlock()
	}, nil
}
