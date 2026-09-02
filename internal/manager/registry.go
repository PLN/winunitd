package manager

import (
	"context"
	"fmt"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/registry"
	"github.com/PLN/winunitd/internal/unit"
)

func (m *Manager) armRegistry(u *unit.Unit) error {
	if m == nil || u == nil || u.Registry == nil {
		return nil
	}
	if len(u.Registry.Changed) == 0 {
		return fmt.Errorf("%s: RegistryChanged is required", core.ReasonConfiguration)
	}
	m.disarmHub(u.Name)

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
			closeWatchers(toWatchIO(opened))
			return fmt.Errorf("%s: %w", core.ReasonConfiguration, err)
		}
		opened = append(opened, w)
	}
	if err := m.installHub(u.Name, toWatchIO(opened), cancel, false); err != nil {
		return err
	}
	for _, w := range opened {
		go m.runWatch(ctx, u.Name, "registry key is missing", w, m.onRegistryChanged)
	}
	return nil
}

func (m *Manager) onRegistryChanged(name string) {
	m.startHubCompanion(name, func(u *unit.Unit) string {
		if u != nil && u.Registry != nil {
			return u.Registry.Unit
		}
		return ""
	})
}
