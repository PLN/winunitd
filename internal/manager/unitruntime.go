package manager

import (
	"context"
	"log"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/unit"
)

// unitRuntime is all per-unit manager state. Removing a name from
// Manager.units tears this down in one place (issue #26).
type unitRuntime struct {
	unit    *unit.Unit
	enabled bool
	targets []string

	state         core.State
	sub           core.Substate
	gen           uint64
	proc          runtime.Process
	notify        *notifyRuntime
	watchdog      context.CancelFunc
	restartCancel context.CancelFunc
	stopping      bool
	terminated    bool
	invocation    string
	regWatch      *registryRuntime
	err           string
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
	watchdog context.CancelFunc
	notify   *notifyRuntime
	restart  context.CancelFunc
	reg      *registryRuntime
}

// detachAsync takes watchdog, notify, restart timer, and registry watch.
// It does not Stop the process (DESIGN.md §33: vanished units are not stopped).
func (rt *unitRuntime) detachAsync() unitTeardown {
	if rt == nil {
		return unitTeardown{}
	}
	td := unitTeardown{
		watchdog: rt.watchdog,
		notify:   rt.notify,
		restart:  rt.restartCancel,
		reg:      rt.regWatch,
	}
	rt.watchdog = nil
	rt.notify = nil
	rt.restartCancel = nil
	rt.regWatch = nil
	return td
}

func (td unitTeardown) cancelNonblocking() {
	if td.restart != nil {
		td.restart()
	}
	if td.watchdog != nil {
		td.watchdog()
	}
	if td.reg != nil && td.reg.cancel != nil {
		td.reg.cancel()
	}
}

func (td unitTeardown) closeBlocking() {
	if td.notify != nil {
		td.notify.Close()
	}
	if td.reg != nil {
		closeWatches(td.reg.watches)
	}
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
