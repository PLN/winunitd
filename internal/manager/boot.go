package manager

import (
	"context"

	"github.com/PLN/winunitd/internal/protocol"
)

// Boot starts default.target, which pulls in enabled Wants=/Requires=
// (DESIGN.md §12). Units on disk that are not enabled are not started.
func (m *Manager) Boot(ctx context.Context) (*protocol.UnitResult, error) {
	return m.Start(ctx, DefaultTarget)
}
