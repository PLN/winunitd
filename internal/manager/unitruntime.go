package manager

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/unit"
)

// unitRuntime is all per-unit manager state. Removing a name from
// Manager.units tears this down in one place (issue #26).
type unitRuntime struct {
	unit           *unit.Unit
	invocationUnit *unit.Unit // captured service definition; reload only replaces unit
	enabled        bool
	targets        []string
	unavailable    bool // latest reload has no valid configuration for this record
	operations     int  // in-flight lifecycle calls retain the record across reload

	state         core.State
	sub           core.Substate
	gen           uint64
	stopEpoch     uint64          // invalidates starts admitted before a stop request
	proc          runtime.Process // whoever clears this owns job.Kill+Close (#25)
	notify        *notifyRuntime
	watchdog      context.CancelFunc
	startCancel   context.CancelFunc
	restartCancel context.CancelFunc
	stopping      bool
	terminated    bool
	stopUncertain bool
	invocation    string
	hub           *watchRuntime
	err           string
	startTimes    []time.Time
}

// ownedUnit selects the definition that controls an existing invocation. Unit
// definitions are immutable after loading. Callers hold the manager mutex.
func (rt *unitRuntime) ownedUnit() *unit.Unit {
	if rt.invocationUnit != nil {
		return rt.invocationUnit
	}
	return rt.unit
}

// step applies a lifecycle event. Illegal transitions are logged and
// not applied (issue #35): the live proc is not rewritten as Active or
// Failed just because an event arrived from the wrong from-state.
func (rt *unitRuntime) step(ev core.Event) bool {
	if rt == nil {
		return false
	}
	st, sub, err := core.Step(rt.state, rt.sub, ev)
	if err != nil {
		name := ""
		if rt.unit != nil {
			name = rt.unit.Name
		}
		logIllegalTransition(name, err)
		return false
	}
	rt.state = st
	rt.sub = sub
	return true
}

func logIllegalTransition(name string, err error) {
	if name == "" {
		name = "?"
	}
	log.Printf("winunitd: %s: %v (not applied)", name, err)
}

// sameOp reports whether this runtime still owns the lifecycle op that
// captured gen. If a different proc is now installed, the captured op
// must not apply a terminal state (issue #24; same idea as watch()).
func (rt *unitRuntime) sameOp(gen uint64, proc runtime.Process) bool {
	if rt == nil || rt.gen != gen {
		return false
	}
	if rt.proc != nil && rt.proc != proc {
		return false
	}
	return true
}

func (rt *unitRuntime) cancelRestart() {
	if rt == nil || rt.restartCancel == nil {
		return
	}
	c := rt.restartCancel
	rt.restartCancel = nil
	c()
}

// unitTeardown holds async control handles taken off a unitRuntime so
// dropping or closing the unit cannot relaunch or leave a stale substate.
type unitTeardown struct {
	start    context.CancelFunc
	watchdog context.CancelFunc
	notify   *notifyRuntime
	restart  context.CancelFunc
	hub      *watchRuntime
}

// detachAsync takes watchdog, notify, restart timer, and hub watches.
// It does not Stop the process (DESIGN.md §33: vanished units are not stopped).
func (rt *unitRuntime) detachAsync() unitTeardown {
	if rt == nil {
		return unitTeardown{}
	}
	td := unitTeardown{
		start:    rt.startCancel,
		watchdog: rt.watchdog,
		notify:   rt.notify,
		restart:  rt.restartCancel,
		hub:      rt.hub,
	}
	rt.watchdog = nil
	rt.startCancel = nil
	rt.notify = nil
	rt.restartCancel = nil
	rt.hub = nil
	return td
}

func (td unitTeardown) cancelNonblocking() {
	if td.start != nil {
		td.start()
	}
	if td.restart != nil {
		td.restart()
	}
	if td.watchdog != nil {
		td.watchdog()
	}
	if td.hub != nil && td.hub.cancel != nil {
		td.hub.cancel()
	}
}

func (td unitTeardown) closeBlocking() error {
	var result error
	if td.notify != nil {
		result = td.notify.Close()
	}
	if td.hub != nil {
		result = errors.Join(result, td.hub.stop())
	}
	return result
}

func (m *Manager) runtimeLocked(name string) *unitRuntime {
	if m == nil || m.units == nil {
		return nil
	}
	return m.units[name]
}

func (m *Manager) procOfLocked(name string) runtime.Process {
	if rt := m.runtimeLocked(name); rt != nil {
		return rt.proc
	}
	return nil
}

func (m *Manager) errOfLocked(name string) string {
	if rt := m.runtimeLocked(name); rt != nil {
		return rt.err
	}
	return ""
}

func (m *Manager) stoppingOfLocked(name string) bool {
	if rt := m.runtimeLocked(name); rt != nil {
		return rt.stopping
	}
	return false
}

func (m *Manager) setErrLocked(name, msg string) {
	if rt := m.runtimeLocked(name); rt != nil {
		rt.err = msg
	}
}

func (m *Manager) clearErrLocked(name string) {
	m.setErrLocked(name, "")
}
