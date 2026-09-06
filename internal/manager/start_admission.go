package manager

import (
	"context"
	"errors"

	"github.com/PLN/winunitd/internal/protocol"
)

const DefaultMaxStartTransactions = 32

var errStartCapacity = protocol.ErrFailed("start transaction capacity exhausted")

// Call with m.mu held. Waiters recheck source identity and capacity on wake.
func (m *Manager) signalStartCapacityLocked() {
	if m.startCapacityChanged != nil {
		close(m.startCapacityChanged)
	}
	m.startCapacityChanged = make(chan struct{})
}

// Native watch loops already serialize notifications per owned watch handle.
// Keep a rejected activation in that loop instead of spawning retry workers.
func (m *Manager) startFromWatch(ctx context.Context, target string, origin *watchOrigin) {
	for {
		m.mu.Lock()
		if !origin.validLocked(m) {
			m.mu.Unlock()
			return
		}
		if m.startCapacityChanged == nil {
			m.startCapacityChanged = make(chan struct{})
		}
		changed := m.startCapacityChanged
		m.mu.Unlock()
		_, err := m.startFromOrigin(ctx, target, origin)
		if !errors.Is(err, errStartCapacity) {
			return
		}
		m.mu.Lock()
		m.capacityWaiters++
		m.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-changed:
		}
		m.mu.Lock()
		m.capacityWaiters--
		m.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
	}
}
