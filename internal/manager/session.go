package manager

import (
	"context"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/runtime"
)

// SyncGraphicalSession starts or stops graphical-session.target so it is
// Active while this SID has a suitable interactive session and Inactive
// when none (DESIGN.md §11, §16). Linger-without-session leaves the
// target inactive; linger units that do not require a session keep
// running. No-op on the system manager. SessionPolicy is not implemented.
func (m *Manager) SyncGraphicalSession(ctx context.Context) error {
	if m == nil || !m.cfg.UserScope {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	m.session.Lock()
	defer m.session.Unlock()

	m.mu.Lock()
	if _, ok := m.units[GraphicalSessionTarget]; !ok {
		m.mu.Unlock()
		return nil
	}
	st := m.stateOfLocked(GraphicalSessionTarget)
	m.mu.Unlock()

	want := m.hasInteractiveSession()
	if want {
		if st == core.Active || st == core.Activating {
			return nil
		}
		_, err := m.Start(ctx, GraphicalSessionTarget)
		return err
	}
	if st == core.Inactive || st == core.Deactivating {
		return nil
	}
	_, err := m.Stop(GraphicalSessionTarget)
	return err
}

// WatchGraphicalSession applies SyncGraphicalSession on each session
// change until ctx is done or ch is closed. The payload is a wakeup:
// presence is re-read from HasInteractiveSession (SIDHasInteractiveSession
// in production). Tests inject this channel.
func (m *Manager) WatchGraphicalSession(ctx context.Context, ch <-chan runtime.SessionChange) {
	if m == nil || ch == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ch:
			if !ok {
				return
			}
			_ = m.SyncGraphicalSession(ctx)
		}
	}
}
