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
	if existing := m.evtWatches[u.Name]; existing != nil {
		delete(m.evtWatches, u.Name)
		go func() {
			existing.cancel()
			closeSubs(existing.subs)
		}()
	}
	if m.evtWatches == nil {
		m.evtWatches = make(map[string]*eventLogRuntime)
	}
	m.evtWatches[u.Name] = rt
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
	if _, watching := m.evtWatches[name]; !watching {
		m.mu.Unlock()
		return
	}
	ld := m.units[name]
	activated := ""
	if ld != nil && ld.unit != nil && ld.unit.EventLog != nil {
		activated = ld.unit.EventLog.Unit
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

func (m *Manager) failEventLogWatch(name string, err error) {
	m.mu.Lock()
	rt, ok := m.evtWatches[name]
	if !ok {
		m.mu.Unlock()
		return
	}
	delete(m.evtWatches, name)
	if m.stateOfLocked(name) == core.Active || m.stateOfLocked(name) == core.Activating {
		st, sub := core.Step(m.stateOfLocked(name), m.subOfLocked(name), core.EventStartFailed)
		m.states[name] = st
		m.subs[name] = sub
		m.errors[name] = err.Error()
	}
	m.mu.Unlock()
	if rt != nil {
		rt.cancel()
		closeSubs(rt.subs)
	}
}

func (m *Manager) disarmEventLog(name string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	rt := m.evtWatches[name]
	delete(m.evtWatches, name)
	m.mu.Unlock()
	if rt == nil {
		return
	}
	rt.cancel()
	closeSubs(rt.subs)
}

func (m *Manager) disarmAllEventLog() {
	if m == nil {
		return
	}
	m.mu.Lock()
	all := m.evtWatches
	m.evtWatches = make(map[string]*eventLogRuntime)
	m.mu.Unlock()
	for _, rt := range all {
		if rt == nil {
			continue
		}
		rt.cancel()
		closeSubs(rt.subs)
	}
}

func (m *Manager) syncEventLogLocked() {
	keep := make(map[string]bool)
	var toArm []*unit.Unit
	for name, ld := range m.units {
		if ld == nil || ld.unit == nil || ld.unit.Kind != unit.KindEventLog {
			continue
		}
		if m.stateOfLocked(name) != core.Active {
			continue
		}
		keep[name] = true
		toArm = append(toArm, ld.unit)
	}
	var stale []*eventLogRuntime
	for name, rt := range m.evtWatches {
		if keep[name] {
			continue
		}
		stale = append(stale, rt)
		delete(m.evtWatches, name)
	}
	go func() {
		for _, rt := range stale {
			if rt == nil {
				continue
			}
			rt.cancel()
			closeSubs(rt.subs)
		}
	}()
	_ = toArm
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
