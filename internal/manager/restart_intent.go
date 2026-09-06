package manager

import "github.com/PLN/winunitd/internal/core"

// restartOrigin prevents a pending explicit restart from undoing a later stop.
// It also protects queued dependency launches after the start phase is admitted.
type restartOrigin struct {
	members map[string]restartMember
}

type restartMember struct {
	record    *unitRuntime
	stopEpoch uint64
}

func (o *restartOrigin) validLocked(m *Manager) bool {
	if m.closed {
		return false
	}
	for name, member := range o.members {
		rt := m.units[name]
		if rt != member.record || rt == nil || rt.stopEpoch != member.stopEpoch {
			return false
		}
	}
	return true
}

// Explicit restarts retain explicit-start policy; they are not trigger retries.
func (*restartOrigin) countsStartLimit() bool { return false }

// Capture participating active/activating members before teardown. PartOf
// members need explicit roots because PlanStart does not pull reverse edges.
func (m *Manager) planRestartLocked(g *core.Graph, name string) (*core.Transaction, []string, *restartOrigin, error) {
	stop, err := g.PlanStop(name)
	if err != nil {
		return nil, nil, nil, err
	}
	origin := &restartOrigin{members: make(map[string]restartMember)}
	roots := []string{name}
	for _, member := range stop.Units() {
		rt := m.units[member]
		if rt == nil {
			continue
		}
		origin.members[member] = restartMember{record: rt, stopEpoch: rt.stopEpoch + 1}
		if member != name && (rt.state == core.Active || rt.state == core.Activating) {
			roots = append(roots, member)
		}
	}
	return stop, roots, origin, nil
}
