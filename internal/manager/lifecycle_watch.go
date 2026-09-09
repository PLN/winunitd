package manager

import (
	"fmt"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/unit"
)

// Watch adapters transfer opened handles here before publishing activation.
// Cleanup remains owned until its exact handle reports completion.
func (m *Manager) acceptHub(h *watchRuntime) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	name := h.unit.Name
	rt := m.units[name]
	if rt == nil || rt.unavailable || rt.stopping || m.closed || rt.hub != nil {
		return fmt.Errorf("unit %q is unavailable, already watched, or manager is closed", name)
	}
	h.gen = rt.gen
	rt.hub = h
	return nil
}

func (m *Manager) acceptHubFailure(event hubCleanup) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	rt := m.units[event.name]
	h := event.hub
	// Explicit stop owns cleanup after admission; reload reconciliation owns removed
	// hubs, and manager shutdown owns its retained hubs. A mismatched generation
	// belongs to the newer owner. Late observations cannot replace those decisions.
	if rt == nil || h == nil || rt.hub != h || rt.gen != h.gen || rt.stopping || rt.unavailable || m.closed {
		return false
	}
	rt.stopUncertain = true
	if rt.state == core.Active || rt.state == core.Activating {
		if rt.step(core.EventStartFailed) {
			rt.err = event.err.Error()
		}
	}
	return true
}

func (m *Manager) acceptHubDisarm(name string) *watchRuntime {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rt := m.units[name]; rt != nil && rt.hub != nil {
		rt.stopUncertain = true
		return rt.hub
	}
	return nil
}

// Partial opens cannot be dropped when closing fails. When the unit cannot own
// them, register manager-close ownership before starting any cancellation or I/O.
func (m *Manager) retainHubDisposal(name string, h *watchRuntime) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rt := m.units[name]; rt != nil && rt.hub == nil && !m.closed {
		rt.hub = h
		rt.stopUncertain = true
		return true
	}
	m.closePending = append(m.closePending, unitTeardown{hub: h})
	return false
}

func (m *Manager) applyPendingHubCleanup(h *watchRuntime, err error) {
	if err != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.closePending[:0]
	for _, td := range m.closePending {
		if td.hub != h {
			kept = append(kept, td)
		}
	}
	m.closePending = kept
}

// The caller commits configuration under the same lock. Cancellation suppresses
// callbacks immediately; blocking watch close is performed by the worker.
func (m *Manager) reconcileHubsLocked() []hubCleanup {
	keep := make(map[string]bool)
	for name, rt := range m.units {
		if rt == nil || rt.unit == nil {
			continue
		}
		switch rt.unit.Kind {
		case unit.KindRegistry, unit.KindEventLog, unit.KindPath:
			// The adapter may have installed its handles before the start
			// transaction publishes Active. Keep that exact owned generation.
			publishing := rt.operations > 0 && rt.hub != nil && rt.hub.gen == rt.gen && !rt.stopping && !rt.stopUncertain
			if (rt.state == core.Active || publishing) && !rt.unavailable {
				keep[name] = true
			}
		}
	}
	var stale []hubCleanup
	for name, rt := range m.units {
		if rt == nil || rt.hub == nil {
			continue
		}
		if keep[name] {
			continue
		}
		stale = append(stale, hubCleanup{name: name, hub: rt.hub})
		rt.stopUncertain = true
		if rt.hub.cancel != nil {
			rt.hub.cancel()
		}
	}
	return stale
}

// A predicate is observed outside the mutex and applied only to its exact armed
// generation. Its rising edge may then request an origin-validated activation.
func (m *Manager) applyPathPredicate(name string, h *watchRuntime, satisfied bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	origin := &watchOrigin{name: name, hub: h}
	if !origin.validLocked(m) || m.stateOfLocked(name) != core.Active {
		return false
	}
	was := h.existsSatisfied
	h.existsSatisfied = satisfied
	return satisfied && !was
}
