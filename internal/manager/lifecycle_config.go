package manager

import (
	"fmt"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/unit"
)

// Configuration decisions execute under m.mu after file parsing and persistence.
// Records retain invocation ownership across accepted definition replacement.
func (m *Manager) replaceLocked(units []*unit.Unit, g *core.Graph, links map[string][]string) []unitTeardown {
	next := make(map[string]*unitRuntime, len(units))
	keep := make(map[string]bool, len(units))
	for _, u := range units {
		name := core.NormalizeName(u.Name)
		keep[name] = true
		rt := m.units[name]
		if rt == nil {
			rt = &unitRuntime{state: core.Inactive}
		}
		targets := enabledTargetsFrom(links, name)
		rt.unit = u
		rt.unavailable = false
		rt.enabled = len(targets) > 0
		rt.targets = targets
		next[name] = rt
	}
	var dropped []unitTeardown
	for name, rt := range m.units {
		if keep[name] {
			continue
		}
		if rt.retainWithoutConfig() {
			rt.unavailable = true
			rt.enabled = false
			rt.targets = nil
			rt.cancelRestart()
			next[name] = rt
			continue
		}
		td := rt.detachAsync()
		td.cancelNonblocking()
		dropped = append(dropped, td)
	}
	m.units = next
	m.signalStartCapacityLocked()
	m.graph = g
	m.acceptConfigRevisionLocked()
	// Live and in-flight records survive independently of configuration files.
	// Unowned vanished units are dropped and their async controls cancelled.
	m.syncTimersLocked()
	m.syncHubsLocked()
	return dropped
}

// The namespace prevents identities from colliding across daemon instances.
// Revisions identify accepted definitions plus graph/enablement, not content
// hashes. Assign with the graph swap while m.mu is held.
func (m *Manager) acceptConfigRevisionLocked() {
	m.configSequence++
	m.configRevision = fmt.Sprintf("%s/%d", m.configNamespace, m.configSequence)
	for _, rt := range m.units {
		if rt.unavailable {
			rt.configRevision = ""
		} else {
			rt.configRevision = m.configRevision
		}
	}
}

func (m *Manager) acceptEnabledGraphLocked(g *core.Graph, links map[string][]string) {
	m.graph = g
	m.acceptConfigRevisionLocked()
	for name, rt := range m.units {
		targets := enabledTargetsFrom(links, name)
		rt.enabled = len(targets) > 0
		rt.targets = targets
	}
}

// Reload planning only needs the definitions whose ownership prevents removal.
// Loaded definitions are protected by configMu. Lifecycle ownership is not;
// validate the exact retained set again while holding m.mu before publication.
type reloadOwnership struct {
	revision string
	retained map[string]*unit.Unit
}

func (m *Manager) captureReloadOwnershipLocked(accepted map[string]bool) reloadOwnership {
	snapshot := reloadOwnership{revision: m.configRevision, retained: make(map[string]*unit.Unit)}
	for name, rt := range m.units {
		if !accepted[name] && rt != nil && rt.retainWithoutConfig() {
			snapshot.retained[name] = rt.unit
		}
	}
	return snapshot
}

func (m *Manager) reloadOwnershipCurrentLocked(accepted map[string]bool, snapshot reloadOwnership) bool {
	if m.configRevision != snapshot.revision {
		return false
	}
	count := 0
	for name, rt := range m.units {
		if !accepted[name] && rt != nil && rt.retainWithoutConfig() {
			if u, ok := snapshot.retained[name]; !ok || u != rt.unit {
				return false
			}
			count++
		}
	}
	return count == len(snapshot.retained)
}
