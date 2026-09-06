package manager

import (
	"context"
	"errors"
	"fmt"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/pathwatch"
	"github.com/PLN/winunitd/internal/unit"
)

func (m *Manager) armPath(u *unit.Unit) error {
	if m == nil || u == nil || u.PathWatch == nil {
		return nil
	}
	if len(u.PathWatch.Changed) == 0 && len(u.PathWatch.Exists) == 0 {
		return fmt.Errorf("%s: PathChanged or PathExists is required", core.ReasonConfiguration)
	}
	if err := m.disarmHub(u.Name); err != nil {
		return err
	}

	open := m.pathOpen
	if open == nil {
		open = pathwatch.OpenWatch
	}
	existsOpen := m.pathExistsOpen
	if existsOpen == nil {
		existsOpen = pathwatch.OpenExistsWatch
	}
	ctx, cancel := context.WithCancel(context.Background())
	var opened []pathwatch.Watch
	var changed []pathwatch.Watch
	for _, spec := range u.PathWatch.Changed {
		w, err := open(spec)
		if w != nil {
			opened = append(opened, w)
		}
		if err != nil {
			cancel()
			cleanupErr := m.disposeHub(u.Name, &watchRuntime{watches: toWatchIO(opened)})
			return errors.Join(fmt.Errorf("%s: %w", core.ReasonConfiguration, err), cleanupErr)
		}
		changed = append(changed, w)
	}
	var existsWatches []pathwatch.Watch
	for _, spec := range u.PathWatch.Exists {
		w, err := existsOpen(spec)
		if w != nil {
			opened = append(opened, w)
		}
		if err != nil {
			cancel()
			cleanupErr := m.disposeHub(u.Name, &watchRuntime{watches: toWatchIO(opened)})
			return errors.Join(fmt.Errorf("%s: %w", core.ReasonConfiguration, err), cleanupErr)
		}
		existsWatches = append(existsWatches, w)
	}
	satisfied := false
	if len(u.PathWatch.Exists) > 0 {
		ok, err := m.pathExistsAll(u.PathWatch.Exists)
		if err != nil {
			cancel()
			cleanupErr := m.disposeHub(u.Name, &watchRuntime{watches: toWatchIO(opened)})
			return errors.Join(fmt.Errorf("%s: %w", core.ReasonConfiguration, err), cleanupErr)
		}
		satisfied = ok
	}
	h, err := m.installHub(u, toWatchIO(opened), cancel, satisfied)
	if err != nil {
		return err
	}

	for _, w := range changed {
		go m.runWatch(ctx, u.Name, "path is not watchable", h, w, m.onPathChanged)
	}
	for _, w := range existsWatches {
		go m.runWatch(ctx, u.Name, "path is not watchable", h, w, m.onPathExistsForHub)
	}
	if satisfied {
		activated := u.PathWatch.Unit
		go m.startPathCompanion(u.Name, h, activated)
	}
	return nil
}

func (m *Manager) onPathChanged(name string, h *watchRuntime) {
	m.startHubCompanion(name, h, func(u *unit.Unit) string {
		if u != nil && u.PathWatch != nil {
			return u.PathWatch.Unit
		}
		return ""
	})
}

func (m *Manager) onPathExistsMaybe(name string) {
	m.mu.Lock()
	var h *watchRuntime
	if rt := m.units[name]; rt != nil {
		h = rt.hub
	}
	m.mu.Unlock()
	m.onPathExistsForHub(name, h)
}

func (m *Manager) onPathExistsForHub(name string, h *watchRuntime) {
	if m == nil {
		return
	}
	m.mu.Lock()
	st := m.stateOfLocked(name)
	if st != core.Active {
		m.mu.Unlock()
		return
	}
	rt := m.units[name]
	if rt == nil || h == nil || rt.hub != h || rt.gen != h.gen || rt.stopping || rt.unavailable || rt.stopUncertain || m.closed || h.unit == nil || h.unit.PathWatch == nil {
		m.mu.Unlock()
		return
	}
	specs := h.unit.PathWatch.Exists
	activated := h.unit.PathWatch.Unit
	m.mu.Unlock()

	ok, err := m.pathExistsAll(specs)
	if err != nil {
		m.failHub(name, h, fmt.Errorf("%s: %w", core.ReasonConfiguration, err))
		return
	}

	m.mu.Lock()
	rt = m.units[name]
	if rt == nil || rt.hub != h || rt.gen != h.gen || rt.stopping || rt.unavailable || rt.stopUncertain || m.closed || m.stateOfLocked(name) != core.Active {
		m.mu.Unlock()
		return
	}
	was := rt.hub.existsSatisfied
	rt.hub.existsSatisfied = ok
	m.mu.Unlock()
	if ok && !was {
		m.startPathCompanion(name, h, activated)
	}
}

func (m *Manager) startPathCompanion(name string, h *watchRuntime, activated string) {
	if activated == "" {
		return
	}
	_, _ = m.startFromWatch(context.Background(), activated, &watchOrigin{name: name, hub: h})
}

func (m *Manager) pathExistsAll(specs []pathwatch.Spec) (bool, error) {
	if len(specs) == 0 {
		return false, nil
	}
	fn := m.pathExists
	if fn == nil {
		fn = pathwatch.Exists
	}
	for _, s := range specs {
		ok, err := fn(s)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}
