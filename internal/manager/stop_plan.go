package manager

import (
	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/unit"
)

// Stop membership and ordering belong to the invocation being torn down.
// The accepted configuration graph is only for new starts. Callers hold m.mu;
// definitions are immutable and planning performs no native or storage work.
func (m *Manager) stopGraphLocked() (*core.Graph, error) {
	definitions := make([]*unit.Unit, 0, len(m.units))
	for _, rt := range m.units {
		if rt == nil {
			continue
		}
		u := rt.unit
		if rt.state != core.Inactive || rt.retainWithoutConfig() || rt.restartCancel != nil {
			u = rt.ownedUnit()
		}
		if u != nil {
			definitions = append(definitions, u)
		}
	}
	return core.Build(definitions)
}
