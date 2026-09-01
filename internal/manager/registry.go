package manager

import (
	"context"
	"fmt"
	"sync"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/registry"
	"github.com/PLN/winunitd/internal/unit"
)

type registryRuntime struct {
	cancel  context.CancelFunc
	watches []registry.Watch
}

func (m *Manager) armRegistry(u *unit.Unit) error {
	if m == nil || u == nil || u.Registry == nil {
		return nil
	}
	if len(u.Registry.Changed) == 0 {
		return fmt.Errorf("%s: RegistryChanged is required", core.ReasonConfiguration)
	}
	m.disarmRegistry(u.Name)

	open := m.regOpen
	if open == nil {
		open = registry.OpenWatch
	}
	ctx, cancel := context.WithCancel(context.Background())
	var opened []registry.Watch
	for _, key := range u.Registry.Changed {
		w, err := open(key)
		if err != nil {
			cancel()
			closeWatches(opened)
			return fmt.Errorf("%s: %w", core.ReasonConfiguration, err)
		}
		opened = append(opened, w)
	}
	rt := &registryRuntime{cancel: cancel, watches: opened}
	m.mu.Lock()
	unitRT := m.units[u.Name]
	if unitRT == nil {
		m.mu.Unlock()
		cancel()
		closeWatches(opened)
		return fmt.Errorf("unit %q is not loaded", u.Name)
	}
	if existing := unitRT.regWatch; existing != nil {
		unitRT.regWatch = nil
		go func() {
			existing.cancel()
			closeWatches(existing.watches)
		}()
	}
	unitRT.regWatch = rt
	m.mu.Unlock()

	for _, w := range opened {
		go m.watchRegistry(ctx, u.Name, w)
	}
	return nil
}

func (m *Manager) watchRegistry(ctx context.Context, name string, w registry.Watch) {
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
					m.failRegistryWatch(name, fmt.Errorf("%s: registry key is missing", core.ReasonConfiguration))
				}
				return
			}
			m.onRegistryChanged(name)
		}
	}
}

func (m *Manager) onRegistryChanged(name string) {
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
	if rt == nil || rt.regWatch == nil {
		m.mu.Unlock()
		return
	}
	activated := ""
	if rt.unit != nil && rt.unit.Registry != nil {
		activated = rt.unit.Registry.Unit
	}
	if activated == "" {
		m.mu.Unlock()
		return
	}
	if proc := m.procOfLocked(activated); proc != nil && proc.Alive() {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	_, _ = m.Start(context.Background(), activated)
}

func (m *Manager) failRegistryWatch(name string, err error) {
	m.mu.Lock()
	rt := m.units[name]
	if rt == nil || rt.regWatch == nil {
		m.mu.Unlock()
		return
	}
	reg := rt.regWatch
	rt.regWatch = nil
	if rt.state == core.Active || rt.state == core.Activating {
		rt.step(core.EventStartFailed)
		rt.err = err.Error()
	}
	m.mu.Unlock()
	if reg != nil {
		reg.cancel()
		closeWatches(reg.watches)
	}
}

func (m *Manager) disarmRegistry(name string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	var reg *registryRuntime
	if rt := m.units[name]; rt != nil {
		reg = rt.regWatch
		rt.regWatch = nil
	}
	m.mu.Unlock()
	if reg == nil {
		return
	}
	reg.cancel()
	closeWatches(reg.watches)
}

func (m *Manager) syncRegistryLocked() {
	keep := make(map[string]bool)
	for name, rt := range m.units {
		if rt == nil || rt.unit == nil || rt.unit.Kind != unit.KindRegistry {
			continue
		}
		if rt.state != core.Active {
			continue
		}
		keep[name] = true
	}
	var stale []*registryRuntime
	for name, rt := range m.units {
		if rt == nil || rt.regWatch == nil {
			continue
		}
		if keep[name] {
			continue
		}
		stale = append(stale, rt.regWatch)
		rt.regWatch = nil
	}
	go func() {
		for _, reg := range stale {
			if reg == nil {
				continue
			}
			reg.cancel()
			closeWatches(reg.watches)
		}
	}()
}

func closeWatches(ws []registry.Watch) {
	var wg sync.WaitGroup
	for _, w := range ws {
		if w == nil {
			continue
		}
		wg.Add(1)
		go func(w registry.Watch) {
			defer wg.Done()
			_ = w.Close()
		}(w)
	}
	wg.Wait()
}
