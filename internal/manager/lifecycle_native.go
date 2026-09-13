package manager

import (
	"context"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/runtime"
)

const (
	maxNativeProbes       = 4
	nativeProbeInterval   = time.Second
	nativeProbeErrorLimit = 4096
)

type nativeProbe struct {
	owner     runtimeIdentity
	epoch     uint64
	scm, task string
}

// One scheduler and at most four uncancellable adapter queries per manager.
// Outstanding queries retain their exact runtime record, even across reload or
// stop. Completion wakes the scheduler without competing for command admission.
func (m *Manager) startNativeProbesLocked() {
	if m.closed || m.nativeProbesDone != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.nativeProbesCancel = cancel
	m.nativeProbesDone = make(chan struct{})
	m.nativeProbesWake = make(chan struct{}, 1)
	m.nativeProbes = make(map[*unitRuntime]*nativeProbe)
	go m.runNativeProbes(ctx)
}

func (m *Manager) runNativeProbes(ctx context.Context) {
	timer := m.clock().Timer(nativeProbeInterval)
	defer timer.Stop()
	for {
		m.mu.Lock()
		if ctx.Err() == nil {
			m.dispatchNativeProbesLocked()
		}
		closed := ctx.Err() != nil
		if len(m.nativeProbes) == 0 && (closed || !m.hasNativeProxiesLocked()) {
			m.nativeProbesCancel()
			close(m.nativeProbesDone)
			m.nativeProbesDone = nil
			m.nativeProbesCancel = nil
			m.mu.Unlock()
			return
		}
		wake := m.nativeProbesWake
		m.mu.Unlock()
		if closed {
			<-wake
			continue
		}
		select {
		case <-ctx.Done():
		case <-wake:
		case <-timer.C():
			timer.Reset(nativeProbeInterval)
		}
	}
}

func (m *Manager) hasNativeProxiesLocked() bool {
	for _, rt := range m.units {
		if rt == nil || rt.stopping || (rt.state != core.Active && rt.state != core.Activating) {
			continue
		}
		u := rt.ownedUnit()
		if scmServiceName(u) != "" || scheduledTaskName(u) != "" {
			return true
		}
	}
	return false
}

// Iterate in rotating name order so a frequently completing query cannot
// starve later names. No queued copies beyond the existing bounded unit map.
func (m *Manager) dispatchNativeProbesLocked() {
	if m.closed || len(m.nativeProbes) == maxNativeProbes {
		return
	}
	now := m.now()
	names := m.names()
	start := sort.SearchStrings(names, m.nativeProbeCursor)
	for n := 0; n < len(names) && len(m.nativeProbes) < maxNativeProbes; n++ {
		name := names[(start+n)%len(names)]
		rt := m.units[name]
		if rt == nil || rt.state != core.Active || rt.stopping || rt.operations != 0 || rt.cleanupPending() || m.nativeProbes[rt] != nil || now.Before(rt.nativeNextProbe) {
			continue
		}
		u := rt.ownedUnit()
		probe := nativeProbe{owner: runtimeIdentity{name: name, record: rt, gen: rt.gen}, epoch: rt.stopEpoch, scm: scmServiceName(u), task: scheduledTaskName(u)}
		if probe.scm == "" && probe.task == "" {
			continue
		}
		rt.operations++
		rt.nativeNextProbe = now.Add(nativeProbeInterval)
		m.nativeProbes[rt] = &probe
		m.nativeProbeCursor = name + "\x00"
		go m.queryNativeProbe(&probe)
	}
}

func (m *Manager) queryNativeProbe(probe *nativeProbe) {
	var inactive bool
	var err error
	if probe.scm != "" {
		var status runtime.SCMStatus
		status, err = m.scm.Query(probe.scm)
		// Paused and pending services may still own running processes.
		inactive = status.State == runtime.SCMStopped
	} else {
		var status runtime.TaskStatus
		status, err = m.tasks.Query(probe.task)
		inactive = status.Instances == 0 && (status.State == runtime.TaskReady || status.State == runtime.TaskDisabled)
	}
	m.completeNativeProbe(probe, inactive, err)
}

func (m *Manager) completeNativeProbe(probe *nativeProbe, inactive bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rt := probe.owner.record
	if m.nativeProbes[rt] != probe {
		return
	}
	delete(m.nativeProbes, rt)
	rt.operations--
	select {
	case m.nativeProbesWake <- struct{}{}:
	default:
	}
	if m.closed || !probe.owner.currentLocked(m) || rt.stopEpoch != probe.epoch || rt.stopping || rt.state != core.Active || rt.operations != 0 {
		return
	}
	rt.nativeProbeError = ""
	if err != nil {
		// An inaccessible or missing target is unknown, not confirmed stopped.
		rt.nativeProbeError = err.Error()
		if len(rt.nativeProbeError) > nativeProbeErrorLimit {
			rt.nativeProbeError = rt.nativeProbeError[:nativeProbeErrorLimit-3]
			for !utf8.ValidString(rt.nativeProbeError) {
				rt.nativeProbeError = rt.nativeProbeError[:len(rt.nativeProbeError)-1]
			}
			rt.nativeProbeError += "..."
		}
		return
	}
	if inactive {
		// Native managers own restart policy. Confirmed inactivity updates this
		// accepted proxy and stops BindsTo dependents, without restarting either.
		rt.state, rt.sub = core.Inactive, core.SubNone
		m.queueBoundStopsLocked(probe.owner.name)
	}
}
