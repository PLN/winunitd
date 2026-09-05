package manager

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/unit"
)

const defaultTaskStartTimeout = 30 * time.Second

func scheduledTaskName(u *unit.Unit) string {
	if u == nil || u.Service == nil || u.Service.Type != unit.TypeScheduledTask {
		return ""
	}
	return u.Service.TaskName
}

func scheduledTaskUserScopeError() error {
	return fmt.Errorf("Type=scheduled-task is only supported in the system manager")
}

func taskStartTimeout(svc *unit.ServiceSpec) time.Duration {
	if svc != nil && svc.TimeoutStartSecSet && svc.TimeoutStartSec > 0 {
		return svc.TimeoutStartSec
	}
	return defaultTaskStartTimeout
}

func (m *Manager) overlayTask(st *protocol.UnitStatus, taskName string) {
	if st == nil || taskName == "" || m.tasks == nil {
		return
	}
	got, err := m.tasks.Query(taskName)
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

func (m *Manager) startTask(ctx context.Context, name string, u *unit.Unit, autoRestart bool) error {
	if m.cfg.UserScope {
		return scheduledTaskUserScopeError()
	}
	if u == nil || u.Service == nil {
		return fmt.Errorf("unit %q has no [Service] section", name)
	}
	svc := u.Service
	if svc.TaskName == "" {
		return fmt.Errorf("TaskName is required for Type=scheduled-task")
	}
	if m.tasks == nil {
		return fmt.Errorf("Task Scheduler client is not configured")
	}
	timeout := taskStartTimeout(svc)
	ctx, cancel := m.clockTimeout(ctx, timeout)
	defer cancel()
	st, err := m.tasks.Start(ctx, svc.TaskName, timeout)
	if err != nil {
		m.maybeRestart(name, core.ExitFailure, svc)
		return err
	}
	_ = st
	m.mu.Lock()
	if autoRestart {
		if rt := m.units[name]; rt != nil && !rt.stopping {
			if rt.step(core.EventStartSucceeded) {
				rt.err = ""
			}
		}
	}
	m.mu.Unlock()
	if m.engine != nil {
		m.engine.UnitActive(name, m.now())
	}
	return nil
}

func (m *Manager) stopTask(ctx context.Context, taskName string, timeout time.Duration) error {
	if taskName == "" {
		return nil
	}
	if m.tasks == nil {
		return fmt.Errorf("Task Scheduler client is not configured")
	}
	key := stopKey{nativeKind: "task", nativeName: strings.ToLower(taskName)}
	return m.awaitStop(ctx, key, timeout, func() error {
		_, err := m.tasks.Stop(ctx, taskName, timeout)
		return err
	})
}
