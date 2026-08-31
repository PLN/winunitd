package manager

import (
	"context"
	"fmt"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/unit"
)

const defaultSCMStartTimeout = 30 * time.Second

func scmServiceName(u *unit.Unit) string {
	if u == nil || u.Service == nil || u.Service.Type != unit.TypeSCM {
		return ""
	}
	return u.Service.ServiceName
}

func scmUserScopeError() error {
	return fmt.Errorf("Type=scm is only supported in the system manager")
}

func scmStartTimeout(svc *unit.ServiceSpec) time.Duration {
	if svc != nil && svc.TimeoutStartSecSet && svc.TimeoutStartSec > 0 {
		return svc.TimeoutStartSec
	}
	return defaultSCMStartTimeout
}

func (m *Manager) overlaySCM(st *protocol.UnitStatus, serviceName string) {
	if st == nil || serviceName == "" || m.scm == nil {
		return
	}
	got, err := m.scm.Query(serviceName)
	if err != nil {
		st.ActiveState = core.Failed.String()
		st.Error = err.Error()
		st.MainPID = 0
		st.InvocationID = ""
		return
	}
	st.ActiveState = got.ActiveState()
	if got.PID > 0 {
		st.MainPID = got.PID
	} else {
		st.MainPID = 0
	}
	st.InvocationID = ""
}

func (m *Manager) startSCM(ctx context.Context, name string, u *unit.Unit, autoRestart bool) error {
	if m.cfg.UserScope {
		return scmUserScopeError()
	}
	if u == nil || u.Service == nil {
		return fmt.Errorf("unit %q has no [Service] section", name)
	}
	svc := u.Service
	if svc.ServiceName == "" {
		return fmt.Errorf("ServiceName is required for Type=scm")
	}
	if m.scm == nil {
		return fmt.Errorf("SCM client is not configured")
	}
	st, err := m.scm.Start(ctx, svc.ServiceName, scmStartTimeout(svc))
	if err != nil {
		m.maybeRestart(name, core.ExitFailure, svc)
		return err
	}
	_ = st
	m.mu.Lock()
	if autoRestart {
		state, sub := core.Step(m.stateOfLocked(name), m.subOfLocked(name), core.EventStartSucceeded)
		m.states[name] = state
		m.subs[name] = sub
		delete(m.errors, name)
	}
	m.mu.Unlock()
	if m.engine != nil {
		m.engine.UnitActive(name, time.Now())
	}
	return nil
}

func (m *Manager) stopSCM(ctx context.Context, serviceName string, timeout time.Duration) error {
	if serviceName == "" {
		return nil
	}
	if m.scm == nil {
		return fmt.Errorf("SCM client is not configured")
	}
	_, err := m.scm.Stop(ctx, serviceName, timeout)
	return err
}
