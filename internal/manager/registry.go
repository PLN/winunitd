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
	if existing := m.regWatches[u.Name]; existing != nil {
		delete(m.regWatches, u.Name)
		go func() {
			existing.cancel()
			closeWatches(existing.watches)
		}()
	}
	if m.regWatches == nil {
		m.regWatches = make(map[string]*registryRuntime)
	}
	m.regWatches[u.Name] = rt
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
	if _, watching := m.regWatches[name]; !watching {
		m.mu.Unlock()
		return
	}
	ld := m.units[name]
	activated := ""
	if ld != nil && ld.unit != nil && ld.unit.Registry != nil {
		activated = ld.unit.Registry.Unit
	}
	if activated == "" {
		m.mu.Unlock()
		return
	}
	if proc := m.procs[activated]; proc != nil && proc.Alive() {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	_, _ = m.Start(context.Background(), activated)
}

func (m *Manager) failRegistryWatch(name string, err error) {
	m.mu.Lock()
	rt, ok := m.regWatches[name]
	if !ok {
		m.mu.Unlock()
		return
	}
	delete(m.regWatches, name)
	if m.stateOfLocked(name) == core.Active || m.stateOfLocked(name) == core.Activating {
		st, sub := core.Step(m.stateOfLocked(name), m.subOfLocked(name), core.EventStartFailed)
		m.states[name] = st
		m.subs[name] = sub
		m.errors[name] = err.Error()
	}
	m.mu.Unlock()
	if rt != nil {
		rt.cancel()
		closeWatches(rt.watches)
	}
}

func (m *Manager) disarmRegistry(name string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	rt := m.regWatches[name]
	delete(m.regWatches, name)
	m.mu.Unlock()
	if rt == nil {
		return
	}
	rt.cancel()
	closeWatches(rt.watches)
}

func (m *Manager) disarmAllRegistry() {
	if m == nil {
		return
	}
	m.mu.Lock()
	all := m.regWatches
	m.regWatches = make(map[string]*registryRuntime)
	m.mu.Unlock()
	for _, rt := range all {
		if rt == nil {
			continue
		}
		rt.cancel()
		closeWatches(rt.watches)
	}
}

func (m *Manager) syncRegistryLocked() {
	keep := make(map[string]bool)
	var toArm []*unit.Unit
	for name, ld := range m.units {
		if ld == nil || ld.unit == nil || ld.unit.Kind != unit.KindRegistry {
			continue
		}
		if m.stateOfLocked(name) != core.Active {
			continue
		}
		keep[name] = true
		toArm = append(toArm, ld.unit)
	}
	var stale []*registryRuntime
	for name, rt := range m.regWatches {
		if keep[name] {
			continue
		}
		stale = append(stale, rt)
		delete(m.regWatches, name)
	}
	go func() {
		for _, rt := range stale {
			if rt == nil {
				continue
			}
			rt.cancel()
			closeWatches(rt.watches)
		}
	}()
	// Re-arm after this lock is released by walking toArm without Open here:
	// startOne already armed Active units; reload of a still-active watch
	// keeps the existing handle (name is in keep).
	_ = toArm
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
