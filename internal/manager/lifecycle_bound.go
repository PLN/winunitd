package manager

import (
	"context"
	"fmt"
	"sort"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/unit"
)

type boundStopMember struct {
	owner runtimeIdentity
	epoch uint64
	unit  *unit.Unit
}

// The caller has accepted an exact peer disappearance under m.mu. Capture
// dependent invocation policy, including definitions retained after reload.
// One pending record per managed name and one worker reserve cleanup progress
// independently of client stop admission. No native work occurs here.
func (m *Manager) queueBoundStopsLocked(peer string) {
	if m.closed {
		return
	}
	var roots []string
	var definitions []*unit.Unit
	for name, rt := range m.units {
		if rt == nil {
			continue
		}
		u := rt.ownedUnit()
		if u == nil {
			continue
		}
		definitions = append(definitions, u)
		if name != peer && !rt.stopping && (rt.state == core.Active || rt.state == core.Activating) && containsBoundName(u.BindsTo, peer) {
			roots = append(roots, name)
		}
	}
	if len(roots) == 0 {
		return
	}
	sort.Strings(roots)
	g, err := core.Build(definitions)
	var plan *core.Transaction
	if err == nil {
		plan, err = g.PlanStop(roots...)
	}
	if err != nil {
		for _, name := range roots {
			rt := m.units[name]
			m.disarmStartLocked(name)
			rt.err = fmt.Sprintf("bound dependency stop plan: %v", err)
		}
		return
	}
	if m.boundStops == nil {
		m.boundStops = make(map[string]boundStopMember)
	}
	for _, name := range plan.Units() {
		rt := m.units[name]
		if rt == nil || (rt.state != core.Active && rt.state != core.Activating && rt.proc == nil && !rt.cleanupPending()) {
			continue
		}
		if old, exists := m.boundStops[name]; exists {
			if old.owner.currentLocked(m) && old.epoch == rt.stopEpoch {
				continue
			}
			old.owner.record.operations--
		}
		m.disarmStartLocked(name)
		rt.operations++
		m.boundStops[name] = boundStopMember{owner: runtimeIdentity{name: name, record: rt, gen: rt.gen}, epoch: rt.stopEpoch, unit: rt.ownedUnit()}
	}
	if len(m.boundStops) != 0 && m.boundStopsDone == nil {
		m.boundStopsDone = make(chan struct{})
		go m.runBoundStops()
	}
}

func containsBoundName(names []string, name string) bool {
	for _, candidate := range names {
		if core.NormalizeName(candidate) == name {
			return true
		}
	}
	return false
}

// BindsTo combined with After requires an active peer, rather than only a
// successful start job (a repeatable oneshot can have completed inactive).
func (m *Manager) boundPeersActiveLocked(u *unit.Unit) bool {
	for _, peer := range u.BindsTo {
		peer = core.NormalizeName(peer)
		if !containsBoundName(u.After, peer) {
			continue
		}
		rt := m.units[peer]
		if rt == nil || rt.state != core.Active || rt.stopping || rt.cleanupPending() {
			return false
		}
	}
	return true
}

func (m *Manager) runBoundStops() {
	for {
		m.mu.Lock()
		members := m.boundStops
		m.boundStops = nil
		if len(members) == 0 {
			close(m.boundStopsDone)
			m.boundStopsDone = nil
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()

		// Build only the accepted member set; a later reload cannot add jobs.
		var definitions []*unit.Unit
		var names []string
		for name, member := range members {
			definitions = append(definitions, member.unit)
			names = append(names, name)
		}
		sort.Strings(names)
		g, err := core.Build(definitions)
		var plan *core.Transaction
		if err == nil {
			plan, err = g.PlanStop(names...)
		}
		m.mu.Lock()
		id := m.beginOperationLocked(names[0], protocol.MethodStop, nil, nil)
		m.operations[id].Origin = "dependency"
		for _, member := range members {
			if member.owner.currentLocked(m) && member.owner.record.stopEpoch == member.epoch {
				member.owner.record.lastOperationID = id
			}
		}
		flight := &startFlight{id: id, done: make(chan struct{})}
		task := m.beginOperationTaskLocked(flight, m.operationTimeoutLocked(nil, plan), nil)
		m.mu.Unlock()
		if err == nil {
			_, err = plan.ExecuteStop(task.ctx, core.StopFunc(func(ctx context.Context, name string) error {
				return m.stopBoundMember(ctx, members[name])
			}))
		}
		m.mu.Lock()
		result := &protocol.UnitResult{Unit: names[0], ActiveState: m.stateOfLocked(names[0]).String()}
		if err != nil {
			for _, member := range members {
				if member.owner.currentLocked(m) && member.owner.record.stopEpoch == member.epoch {
					member.owner.record.err = fmt.Sprintf("bound dependency stop: %v", err)
				}
			}
		}
		m.mu.Unlock()
		m.finishOperationTask(names[0], task, result, err, func() {
			for _, member := range members {
				member.owner.record.operations--
			}
		})
	}
}

func (m *Manager) stopBoundMember(ctx context.Context, member boundStopMember) error {
	unlock, err := m.ops.lockContext(ctx, member.owner.name)
	if err != nil {
		return err
	}
	released := false
	release := func() {
		if !released {
			released = true
			unlock()
		}
	}
	defer release()
	m.mu.Lock()
	current := member.owner.currentLocked(m) && member.owner.record.stopEpoch == member.epoch
	m.mu.Unlock()
	if !current {
		return nil
	}
	_, err = m.stopUnitAfterLock(ctx, member.owner.name, release)
	return err
}
