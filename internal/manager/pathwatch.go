package manager

import (
	"context"
	"fmt"
	"sync"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/pathwatch"
	"github.com/PLN/winunitd/internal/unit"
)

type pathRuntime struct {
	cancel  context.CancelFunc
	watches []pathwatch.Watch
}

func (m *Manager) armPath(u *unit.Unit) error {
	if m == nil || u == nil || u.PathWatch == nil {
		return nil
	}
	if len(u.PathWatch.Changed) == 0 {
		return fmt.Errorf("%s: PathChanged is required", core.ReasonConfiguration)
	}
	m.disarmPath(u.Name)

	open := m.pathOpen
	if open == nil {
		open = pathwatch.OpenWatch
	}
	ctx, cancel := context.WithCancel(context.Background())
	var opened []pathwatch.Watch
	for _, spec := range u.PathWatch.Changed {
		w, err := open(spec)
		if err != nil {
			cancel()
			closePathWatches(opened)
			return fmt.Errorf("%s: %w", core.ReasonConfiguration, err)
		}
		opened = append(opened, w)
	}
	rt := &pathRuntime{cancel: cancel, watches: opened}
	m.mu.Lock()
	unitRT := m.units[u.Name]
	if unitRT == nil {
		m.mu.Unlock()
		cancel()
		closePathWatches(opened)
		return fmt.Errorf("unit %q is not loaded", u.Name)
	}
	if existing := unitRT.pathWatch; existing != nil {
		unitRT.pathWatch = nil
		go func() {
			existing.cancel()
			closePathWatches(existing.watches)
		}()
	}
	unitRT.pathWatch = rt
	m.mu.Unlock()

	for _, w := range opened {
		go m.watchPath(ctx, u.Name, w)
	}
	return nil
}

func (m *Manager) watchPath(ctx context.Context, name string, w pathwatch.Watch) {
	if w == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-w.C():
			if !ok {
				select {
				case <-ctx.Done():
					return
				default:
					m.failPathWatch(name, fmt.Errorf("%s: path is not watchable", core.ReasonConfiguration))
				}
				return
			}
			m.onPathChanged(name)
		}
	}
}

func (m *Manager) onPathChanged(name string) {
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
	if rt == nil || rt.pathWatch == nil {
		m.mu.Unlock()
		return
	}
	activated := ""
	if rt.unit != nil && rt.unit.PathWatch != nil {
		activated = rt.unit.PathWatch.Unit
	}
	if activated == "" {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	// Start goes through per-unit serialization (C3). A still-running
	// Type=simple early-returns in launchUnitOp without bumping gen (C1).
	_, _ = m.Start(context.Background(), activated)
}

func (m *Manager) failPathWatch(name string, err error) {
	m.mu.Lock()
	rt := m.units[name]
	if rt == nil || rt.pathWatch == nil {
		m.mu.Unlock()
		return
	}
	pw := rt.pathWatch
	rt.pathWatch = nil
	if rt.state == core.Active || rt.state == core.Activating {
		if rt.step(core.EventStartFailed) {
			rt.err = err.Error()
		}
	}
	m.mu.Unlock()
	if pw != nil {
		pw.cancel()
		closePathWatches(pw.watches)
	}
}

func (m *Manager) disarmPath(name string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	var pw *pathRuntime
	if rt := m.units[name]; rt != nil {
		pw = rt.pathWatch
		rt.pathWatch = nil
	}
	m.mu.Unlock()
	if pw == nil {
		return
	}
	pw.cancel()
	closePathWatches(pw.watches)
}

func (m *Manager) syncPathLocked() {
	keep := make(map[string]bool)
	for name, rt := range m.units {
		if rt == nil || rt.unit == nil || rt.unit.Kind != unit.KindPath {
			continue
		}
		if rt.state != core.Active {
			continue
		}
		keep[name] = true
	}
	var stale []*pathRuntime
	for name, rt := range m.units {
		if rt == nil || rt.pathWatch == nil {
			continue
		}
		if keep[name] {
			continue
		}
		stale = append(stale, rt.pathWatch)
		rt.pathWatch = nil
	}
	go func() {
		for _, pw := range stale {
			if pw == nil {
				continue
			}
			pw.cancel()
			closePathWatches(pw.watches)
		}
	}()
}

func closePathWatches(ws []pathwatch.Watch) {
	var wg sync.WaitGroup
	for _, w := range ws {
		if w == nil {
			continue
		}
		wg.Add(1)
		go func(w pathwatch.Watch) {
			defer wg.Done()
			_ = w.Close()
		}(w)
	}
	wg.Wait()
}
