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
		if err != nil {
			cancel()
			cleanupErr := m.disposeHub(u.Name, &watchRuntime{watches: toWatchIO(opened)})
			return errors.Join(fmt.Errorf("%s: %w", core.ReasonConfiguration, err), cleanupErr)
		}
		opened = append(opened, w)
		changed = append(changed, w)
	}
	var existsWatches []pathwatch.Watch
	for _, spec := range u.PathWatch.Exists {
		w, err := existsOpen(spec)
		if err != nil {
			cancel()
			cleanupErr := m.disposeHub(u.Name, &watchRuntime{watches: toWatchIO(opened)})
			return errors.Join(fmt.Errorf("%s: %w", core.ReasonConfiguration, err), cleanupErr)
		}
		opened = append(opened, w)
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
	if err := m.installHub(u.Name, toWatchIO(opened), cancel, satisfied); err != nil {
		return err
	}

	for _, w := range changed {
		go m.runWatch(ctx, u.Name, "path is not watchable", w, m.onPathChanged)
	}
	for _, w := range existsWatches {
		go m.runWatch(ctx, u.Name, "path is not watchable", w, m.onPathExistsMaybe)
	}
	if satisfied {
		activated := u.PathWatch.Unit
		go m.startPathCompanion(activated)
	}
	return nil
}

func (m *Manager) onPathChanged(name string) {
	m.startHubCompanion(name, func(u *unit.Unit) string {
		if u != nil && u.PathWatch != nil {
			return u.PathWatch.Unit
		}
		return ""
	})
}

func (m *Manager) onPathExistsMaybe(name string) {
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
	if rt == nil || rt.hub == nil || rt.unit == nil || rt.unit.PathWatch == nil {
		m.mu.Unlock()
		return
	}
	specs := rt.unit.PathWatch.Exists
	activated := rt.unit.PathWatch.Unit
	m.mu.Unlock()

	ok, err := m.pathExistsAll(specs)
	if err != nil {
		m.failHub(name, fmt.Errorf("%s: %w", core.ReasonConfiguration, err))
		return
	}

	m.mu.Lock()
	rt = m.units[name]
	if rt == nil || rt.hub == nil || m.stateOfLocked(name) != core.Active {
		m.mu.Unlock()
		return
	}
	was := rt.hub.existsSatisfied
	rt.hub.existsSatisfied = ok
	m.mu.Unlock()
	if ok && !was {
		m.startPathCompanion(activated)
	}
}

func (m *Manager) startPathCompanion(activated string) {
	if activated == "" {
		return
	}
	_, _ = m.Start(context.Background(), activated)
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
