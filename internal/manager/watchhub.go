package manager

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

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
	gen             uint64
	unit            *unit.Unit // immutable definition captured when the watches were opened
	revision        string
	closeMu         sync.Mutex
	cancel          context.CancelFunc
	watches         []watchIO
	existsSatisfied bool
}

func (h *watchRuntime) stop() error {
	if h == nil {
		return nil
	}
	h.closeMu.Lock()
	defer h.closeMu.Unlock()
	if h.cancel != nil {
		h.cancel()
	}
	if err := closeWatchers(h.watches); err != nil {
		return err
	}
	h.watches = nil
	return nil
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

func (m *Manager) installHub(u *unit.Unit, revision string, opened []watchIO, cancel context.CancelFunc, existsSatisfied bool) (*watchRuntime, error) {
	name := u.Name
	rt := &watchRuntime{unit: u, revision: revision, cancel: cancel, watches: opened, existsSatisfied: existsSatisfied}
	m.mu.Lock()
	unitRT := m.units[name]
	if unitRT == nil || unitRT.unavailable || unitRT.stopping || m.closed || unitRT.hub != nil {
		m.mu.Unlock()
		return nil, errors.Join(fmt.Errorf("unit %q is unavailable, already watched, or manager is closed", name), m.disposeHub(name, rt))
	}
	rt.gen = unitRT.gen
	unitRT.hub = rt
	m.mu.Unlock()
	return rt, nil
}

func (m *Manager) runWatch(ctx context.Context, name, failMsg string, h *watchRuntime, w watchIO, onFire func(string, *watchRuntime)) {
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
					m.failHub(name, h, fmt.Errorf("%s: %s", core.ReasonConfiguration, failMsg))
				}
				return
			}
			onFire(name, h)
		}
	}
}

func (m *Manager) failHub(name string, h *watchRuntime, err error) {
	m.mu.Lock()
	rt := m.units[name]
	if rt == nil || h == nil || rt.hub != h || rt.gen != h.gen {
		m.mu.Unlock()
		return
	}
	rt.stopUncertain = true
	if rt.state == core.Active || rt.state == core.Activating {
		if rt.step(core.EventStartFailed) {
			rt.err = err.Error()
		}
	}
	m.mu.Unlock()
	_ = m.closeHub(context.Background(), name, h, defaultStopTimeout)
}

func (m *Manager) disarmHub(name string) error {
	return m.disarmHubContext(context.Background(), name, defaultStopTimeout)
}

func (m *Manager) disarmHubContext(ctx context.Context, name string, timeout time.Duration) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	var h *watchRuntime
	if rt := m.units[name]; rt != nil {
		h = rt.hub
		if h != nil {
			rt.stopUncertain = true
		}
	}
	m.mu.Unlock()
	return m.closeHub(ctx, name, h, timeout)
}

func (m *Manager) closeHub(ctx context.Context, name string, h *watchRuntime, timeout time.Duration) error {
	if h == nil {
		return nil
	}
	if h.cancel != nil {
		h.cancel()
	}
	err := m.stops.wait(ctx, m.clock(), stopKey{hub: h}, timeout, h.stop)
	m.mu.Lock()
	if rt := m.units[name]; rt != nil && rt.hub == h {
		if err == nil {
			rt.hub = nil
			rt.stopUncertain = false
		} else {
			rt.stopUncertain = true
			rt.err = fmt.Sprintf("watch cleanup: %v", err)
		}
	}
	m.mu.Unlock()
	return err
}

// disposeHub keeps partial opens on their unit for stop retry when possible.
// Rejected groups without a unit owner remain in manager close ownership.
func (m *Manager) disposeHub(name string, h *watchRuntime) error {
	m.mu.Lock()
	if rt := m.units[name]; rt != nil && rt.hub == nil && !m.closed {
		rt.hub = h
		rt.stopUncertain = true
		m.mu.Unlock()
		return m.closeHub(context.Background(), name, h, defaultStopTimeout)
	}
	m.mu.Unlock()
	if h.cancel != nil {
		h.cancel()
	}
	m.mu.Lock()
	m.closePending = append(m.closePending, unitTeardown{hub: h})
	m.mu.Unlock()
	err := m.stops.wait(context.Background(), m.clock(), stopKey{hub: h}, defaultStopTimeout, h.stop)
	if err == nil {
		m.mu.Lock()
		kept := m.closePending[:0]
		for _, td := range m.closePending {
			if td.hub != h {
				kept = append(kept, td)
			}
		}
		m.closePending = kept
		m.mu.Unlock()
	}
	return err
}

func (m *Manager) syncHubsLocked() {
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
	type staleHub struct {
		name string
		hub  *watchRuntime
	}
	var stale []staleHub
	for name, rt := range m.units {
		if rt == nil || rt.hub == nil {
			continue
		}
		if keep[name] {
			continue
		}
		stale = append(stale, staleHub{name, rt.hub})
		rt.stopUncertain = true
		if rt.hub.cancel != nil {
			rt.hub.cancel()
		}
	}
	go func() {
		for _, h := range stale {
			_ = m.closeHub(context.Background(), h.name, h.hub, defaultStopTimeout)
		}
	}()
}

// startHubCompanion starts the counterpart unit. A still-running
// Type=simple early-returns in launchUnitOp without bumping gen (C1);
// Start is serialized per unit (C3).
func (m *Manager) startHubCompanion(name string, h *watchRuntime, companion func(*unit.Unit) string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.stateOfLocked(name) != core.Active {
		m.mu.Unlock()
		return
	}
	rt := m.units[name]
	if rt == nil || h == nil || rt.hub != h || rt.stopping || rt.stopUncertain || rt.unavailable || m.closed {
		m.mu.Unlock()
		return
	}
	activated := companion(h.unit)
	m.mu.Unlock()
	if activated == "" {
		return
	}
	m.startFromWatch(context.Background(), activated, &watchOrigin{name: name, hub: h})
}

// watchOrigin binds queued activation to the exact armed watch generation.
// Validate at admission and again after the destination operation gate opens.
type watchOrigin struct {
	name string
	hub  *watchRuntime
}

func (*watchOrigin) countsStartLimit() bool { return true }

func (o *watchOrigin) validLocked(m *Manager) bool {
	rt := m.units[o.name]
	return !m.closed && rt != nil && o.hub != nil && rt.hub == o.hub && rt.gen == o.hub.gen && !rt.stopping && !rt.unavailable && !rt.stopUncertain
}
