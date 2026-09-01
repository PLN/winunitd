package manager

import (
	"context"
	"fmt"
	"sync"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/eventlog"
	"github.com/PLN/winunitd/internal/unit"
)

type eventLogRuntime struct {
	cancel context.CancelFunc
	subs   []eventlog.Subscription
}

func (m *Manager) armEventLog(u *unit.Unit) error {
	if m == nil || u == nil || u.EventLog == nil {
		return nil
	}
	if len(u.EventLog.Triggers) == 0 {
		return fmt.Errorf("%s: EventLogTrigger is required", core.ReasonConfiguration)
	}
	m.disarmEventLog(u.Name)

	open := m.evtOpen
	if open == nil {
		open = eventlog.OpenSubscribe
	}
	ctx, cancel := context.WithCancel(context.Background())
	var opened []eventlog.Subscription
	for _, tr := range u.EventLog.Triggers {
		s, err := open(tr)
		if err != nil {
			cancel()
			closeSubs(opened)
			return fmt.Errorf("%s: %w", core.ReasonConfiguration, err)
		}
		opened = append(opened, s)
	}
	rt := &eventLogRuntime{cancel: cancel, subs: opened}
	m.mu.Lock()
	unitRT := m.units[u.Name]
	if unitRT == nil {
		m.mu.Unlock()
		cancel()
		closeSubs(opened)
		return fmt.Errorf("unit %q is not loaded", u.Name)
	}
	if existing := unitRT.evtWatch; existing != nil {
		unitRT.evtWatch = nil
		go func() {
			existing.cancel()
			closeSubs(existing.subs)
		}()
	}
	unitRT.evtWatch = rt
	m.mu.Unlock()

	for _, s := range opened {
		go m.watchEventLog(ctx, u.Name, s)
	}
	return nil
}

func (m *Manager) watchEventLog(ctx context.Context, name string, s eventlog.Subscription) {
	if s == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-s.C():
			if !ok {
				select {
				case <-ctx.Done():
					return
				default:
					m.failEventLogWatch(name, fmt.Errorf("%s: event log subscribe failed", core.ReasonConfiguration))
				}
				return
			}
			m.onEventLogMatch(name)
		}
	}
}

func (m *Manager) onEventLogMatch(name string) {
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
	if rt == nil || rt.evtWatch == nil {
		m.mu.Unlock()
		return
	}
	activated := ""
	if rt.unit != nil && rt.unit.EventLog != nil {
		activated = rt.unit.EventLog.Unit
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

func (m *Manager) failEventLogWatch(name string, err error) {
	m.mu.Lock()
	rt := m.units[name]
	if rt == nil || rt.evtWatch == nil {
		m.mu.Unlock()
		return
	}
	evt := rt.evtWatch
	rt.evtWatch = nil
	if rt.state == core.Active || rt.state == core.Activating {
		if rt.step(core.EventStartFailed) {
			rt.err = err.Error()
		}
	}
	m.mu.Unlock()
	if evt != nil {
		evt.cancel()
		closeSubs(evt.subs)
	}
}

func (m *Manager) disarmEventLog(name string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	var evt *eventLogRuntime
	if rt := m.units[name]; rt != nil {
		evt = rt.evtWatch
		rt.evtWatch = nil
	}
	m.mu.Unlock()
	if evt == nil {
		return
	}
	evt.cancel()
	closeSubs(evt.subs)
}

func (m *Manager) syncEventLogLocked() {
	keep := make(map[string]bool)
	for name, rt := range m.units {
		if rt == nil || rt.unit == nil || rt.unit.Kind != unit.KindEventLog {
			continue
		}
		if rt.state != core.Active {
			continue
		}
		keep[name] = true
	}
	var stale []*eventLogRuntime
	for name, rt := range m.units {
		if rt == nil || rt.evtWatch == nil {
			continue
		}
		if keep[name] {
			continue
		}
		stale = append(stale, rt.evtWatch)
		rt.evtWatch = nil
	}
	go func() {
		for _, evt := range stale {
			if evt == nil {
				continue
			}
			evt.cancel()
			closeSubs(evt.subs)
		}
	}()
}

func closeSubs(ss []eventlog.Subscription) {
	var wg sync.WaitGroup
	for _, s := range ss {
		if s == nil {
			continue
		}
		wg.Add(1)
		go func(s eventlog.Subscription) {
			defer wg.Done()
			_ = s.Close()
		}(s)
	}
	wg.Wait()
}
