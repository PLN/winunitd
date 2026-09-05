package manager

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/unit"
)

// watchIO is the shared registry / eventlog / path watch surface.
type watchIO interface {
	C() <-chan struct{}
	Close() error
}

// watchRuntime is the armed-hub state for one unit. Registry, eventlog,
// and path all store this on unitRuntime.hub so close/fail/disarm/sync
// share one path (issue #67).
type watchRuntime struct {
	cancel          context.CancelFunc
	watches         []watchIO
	existsSatisfied bool
}

func (h *watchRuntime) stop() {
	if h == nil {
		return
	}
	if h.cancel != nil {
		h.cancel()
	}
	closeWatchers(h.watches)
}

func (h *watchRuntime) stopAsync() {
	if h == nil {
		return
	}
	go h.stop()
}

func closeWatchers(ws []watchIO) error {
	var wg sync.WaitGroup
	errs := make(chan error, len(ws))
	for _, w := range ws {
		if w == nil {
			continue
		}
		wg.Add(1)
		go func(w watchIO) {
			defer wg.Done()
			errs <- w.Close()
		}(w)
	}
	wg.Wait()
	close(errs)
	var result error
	for err := range errs {
		result = errors.Join(result, err)
	}
	return result
}

func toWatchIO[W watchIO](ws []W) []watchIO {
	out := make([]watchIO, len(ws))
	for i, w := range ws {
		out[i] = w
	}
	return out
}

func (m *Manager) installHub(name string, opened []watchIO, cancel context.CancelFunc, existsSatisfied bool) error {
	rt := &watchRuntime{cancel: cancel, watches: opened, existsSatisfied: existsSatisfied}
	m.mu.Lock()
	unitRT := m.units[name]
	if unitRT == nil || unitRT.unavailable || m.closed {
		m.mu.Unlock()
		rt.stop()
		return fmt.Errorf("unit %q is unavailable or manager is closed", name)
	}
	if existing := unitRT.hub; existing != nil {
		unitRT.hub = nil
		existing.stopAsync()
	}
	unitRT.hub = rt
	m.mu.Unlock()
	return nil
}

func (m *Manager) runWatch(ctx context.Context, name, failMsg string, w watchIO, onFire func(string)) {
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
					m.failHub(name, fmt.Errorf("%s: %s", core.ReasonConfiguration, failMsg))
				}
				return
			}
			onFire(name)
		}
	}
}

func (m *Manager) failHub(name string, err error) {
	m.mu.Lock()
	rt := m.units[name]
	if rt == nil || rt.hub == nil {
		m.mu.Unlock()
		return
	}
	h := rt.hub
	rt.hub = nil
	if rt.state == core.Active || rt.state == core.Activating {
		if rt.step(core.EventStartFailed) {
			rt.err = err.Error()
		}
	}
	m.mu.Unlock()
	h.stop()
}

func (m *Manager) disarmHub(name string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	var h *watchRuntime
	if rt := m.units[name]; rt != nil {
		h = rt.hub
		rt.hub = nil
	}
	m.mu.Unlock()
	h.stop()
}

func (m *Manager) syncHubsLocked() {
	keep := make(map[string]bool)
	for name, rt := range m.units {
		if rt == nil || rt.unit == nil {
			continue
		}
		switch rt.unit.Kind {
		case unit.KindRegistry, unit.KindEventLog, unit.KindPath:
			if rt.state == core.Active && !rt.unavailable {
				keep[name] = true
			}
		}
	}
	var stale []*watchRuntime
	for name, rt := range m.units {
		if rt == nil || rt.hub == nil {
			continue
		}
		if keep[name] {
			continue
		}
		stale = append(stale, rt.hub)
		rt.hub = nil
	}
	go func() {
		for _, h := range stale {
			h.stop()
		}
	}()
}

// startHubCompanion starts the counterpart unit. A still-running
// Type=simple early-returns in launchUnitOp without bumping gen (C1);
// Start is serialized per unit (C3).
func (m *Manager) startHubCompanion(name string, companion func(*unit.Unit) string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.stateOfLocked(name) != core.Active {
		m.mu.Unlock()
		return
	}
	rt := m.units[name]
	if rt == nil || rt.hub == nil || rt.unavailable || m.closed {
		m.mu.Unlock()
		return
	}
	activated := companion(rt.unit)
	m.mu.Unlock()
	if activated == "" {
		return
	}
	_, _ = m.Start(context.Background(), activated)
}
