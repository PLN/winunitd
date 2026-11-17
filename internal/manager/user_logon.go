package manager

import (
	"errors"
	"fmt"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/runtime"
)

// A User= unit with UserLogon=session-or-s4u may run on a copy of a signed-in
// session's token. When that session signs out the manager ends the
// invocation, because Windows leaves its session-0 processes running, and
// Restart= decides on it as on a failed exit.

func sessionEndReason(session uint32) string {
	return fmt.Sprintf("session %d, whose logon this invocation used, signed out", session)
}

func (m *Manager) sessionSignedIn(session uint32, sid string) bool {
	if m.cfg.SessionSignedIn != nil {
		return m.cfg.SessionSignedIn(session, sid)
	}
	return runtime.SessionSignedIn(session, sid)
}

func processLogon(proc runtime.Process) (string, uint32) {
	if l, ok := proc.(runtime.AccountLogon); ok {
		return l.AccountLogon()
	}
	return "", 0
}

// beginSessionLaunch collects the sign-outs that arrive while the launcher
// selects a session, before the invocation can say which one it used.
func (m *Manager) beginSessionLaunch(effect *launchEffect) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if effect.owner.currentLocked(m) {
		rt := effect.owner.record
		rt.launchingSession, rt.launchLogoffs = true, nil
	}
}

func (m *Manager) endSessionLaunch(effect *launchEffect) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if effect.owner.currentLocked(m) {
		rt := effect.owner.record
		rt.launchingSession, rt.launchLogoffs = false, nil
	}
}

// adoptLaunchLogon records the logon of an adopted process and notes it in the
// journal. A sign-out of its session delivered during the launch, or one that
// happened before the launch registered and was delivered to nobody, ends the
// start: its job is killed and activation is refused.
func (m *Manager) adoptLaunchLogon(effect *launchEffect, proc runtime.Process, sid string) {
	logon, session := processLogon(proc)
	if logon == "" {
		m.endSessionLaunch(effect)
		return
	}
	m.mu.Lock()
	rt := effect.owner.record
	if !effect.owner.currentLocked(m) || rt.proc != proc {
		rt.launchingSession, rt.launchLogoffs = false, nil
		m.mu.Unlock()
		return
	}
	rt.invocationLogon, rt.invocationSession = logon, session
	missed := false
	if rt.launchingSession {
		for _, id := range rt.launchLogoffs {
			missed = missed || (logon == runtime.LogonSession && id == session)
		}
	}
	rt.launchingSession, rt.launchLogoffs = false, nil
	note := "logon: " + logon
	if logon == runtime.LogonSession {
		note = fmt.Sprintf("logon: session %d", session)
	}
	rt.diagnostics.RecordNote(effect.owner.name, rt.invocation, note)
	if missed {
		m.endSessionLocked(rt)
	}
	m.mu.Unlock()
	if logon != runtime.LogonSession {
		return
	}
	if !missed && !m.sessionSignedIn(session, sid) {
		m.mu.Lock()
		missed = effect.owner.currentLocked(m) && rt.proc == proc && !rt.sessionEnded
		if missed {
			m.endSessionLocked(rt)
		}
		m.mu.Unlock()
	}
	if missed {
		if job := proc.Job(); job != nil {
			_ = job.Kill()
		}
	}
}

// endSessionLocked records that the invocation's session signed out.
func (m *Manager) endSessionLocked(rt *unitRuntime) {
	rt.sessionEnded = true
	name := ""
	if rt.unit != nil {
		name = rt.unit.Name
	}
	rt.diagnostics.RecordNote(name, rt.invocation, sessionEndReason(rt.invocationSession))
}

// sessionEndCause wraps the error of a start that ended because its session
// signed out with the reason. ended reports such a start.
func (m *Manager) sessionEndCause(effect *launchEffect, proc runtime.Process, err error) (error, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rt := effect.owner.record
	if !effect.owner.currentLocked(m) || rt.proc != proc || !rt.sessionEnded {
		return err, false
	}
	reason := sessionEndReason(rt.invocationSession)
	if err == nil {
		return errors.New(reason), true
	}
	return fmt.Errorf("%s: %w", reason, err), true
}

// SessionSignedOut ends every invocation that runs on the logon of session id.
// It is called for each logoff the service sees, also twice for one, and for
// sessions that no invocation uses. Lock, disconnect and reconnect are not
// logoffs and never reach it.
func (m *Manager) SessionSignedOut(id uint32) {
	type ending struct {
		owner runtimeIdentity
		proc  runtime.Process
	}
	var kills []runtime.Job
	var ends []ending
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	for name, rt := range m.units {
		if rt == nil {
			continue
		}
		if rt.launchingSession {
			rt.launchLogoffs = append(rt.launchLogoffs, id)
			continue
		}
		if rt.proc == nil || rt.stopping || rt.cleanupPending() || rt.sessionEnded ||
			rt.invocationLogon != runtime.LogonSession || rt.invocationSession != id {
			continue
		}
		m.endSessionLocked(rt)
		if rt.state == core.Active {
			ends = append(ends, ending{owner: runtimeIdentity{name: name, record: rt, gen: rt.gen}, proc: rt.proc})
		} else if job := rt.proc.Job(); job != nil {
			// A start still waits for exit, readiness or publication; the dead
			// main process fails it, and activation is refused.
			kills = append(kills, job)
		}
	}
	m.mu.Unlock()
	for _, job := range kills {
		_ = job.Kill()
	}
	for _, e := range ends {
		go m.endInvocation(e.owner, func(owner runtimeIdentity) *watchdogEffect { return m.acceptSessionEnd(owner, e.proc) })
	}
}

// acceptSessionEnd mirrors acceptWatchdogFailure for an Active invocation
// whose session signed out. Anything that replaced or stopped it wins.
func (m *Manager) acceptSessionEnd(owner runtimeIdentity, proc runtime.Process) *watchdogEffect {
	m.mu.Lock()
	defer m.mu.Unlock()
	rt := m.units[owner.name]
	if !owner.currentLocked(m) || rt.stopping || m.closed || rt.cleanupPending() || rt.proc == nil || rt.proc != proc || rt.state != core.Active {
		return nil
	}
	if !rt.step(core.EventMainExited) {
		return nil
	}
	m.queueBoundStopsLocked(owner.name)
	rt.err = sessionEndReason(rt.invocationSession)
	rt.terminated = true
	effect := &watchdogEffect{owner: owner, unit: rt.ownedUnit(), process: rt.proc, cancel: rt.watchdog, stopEligible: true, kind: core.ExitFailure, cause: "sign-out"}
	rt.setCleanup(cleanupWorkload, true)
	rt.watchdog = nil
	return effect
}

// stopHelperSessionLocked is the logon an ExecStop helper asks for: the
// session logon while the invocation's session has not been reported signed
// out, S4U otherwise.
func stopHelperSessionLocked(rt *unitRuntime) bool {
	return rt.invocationLogon == runtime.LogonSession && !rt.sessionEnded
}
