package runtime

import (
	"context"
	"fmt"
	"time"
)

// SCM orchestrates an existing named Windows Service (DESIGN.md §51).
// Implementations must not create, change, or delete SCM configuration.
type SCM interface {
	Start(ctx context.Context, name string, timeout time.Duration) (SCMStatus, error)
	Stop(ctx context.Context, name string, timeout time.Duration) (SCMStatus, error)
	Query(name string) (SCMStatus, error)
}

// SCMState is a SERVICE_STATUS.dwCurrentState value.
type SCMState uint32

const (
	SCMStopped         SCMState = 1
	SCMStartPending    SCMState = 2
	SCMStopPending     SCMState = 3
	SCMRunning         SCMState = 4
	SCMContinuePending SCMState = 5
	SCMPausePending    SCMState = 6
	SCMPaused          SCMState = 7
)

// SCMStatus is QueryServiceStatusEx mapped into winunitd terms.
type SCMStatus struct {
	State SCMState
	PID   int
}

// ActiveState maps SCM dwCurrentState onto existing ActiveState strings
// (inactive / activating / active / deactivating / failed).
func (s SCMStatus) ActiveState() string {
	switch s.State {
	case SCMRunning:
		return "active"
	case SCMStopped, SCMPaused:
		return "inactive"
	case SCMStartPending, SCMContinuePending:
		return "activating"
	case SCMStopPending, SCMPausePending:
		return "deactivating"
	default:
		return "failed"
	}
}

func (s SCMState) String() string {
	switch s {
	case SCMStopped:
		return "stopped"
	case SCMStartPending:
		return "start-pending"
	case SCMStopPending:
		return "stop-pending"
	case SCMRunning:
		return "running"
	case SCMContinuePending:
		return "continue-pending"
	case SCMPausePending:
		return "pause-pending"
	case SCMPaused:
		return "paused"
	default:
		return fmt.Sprintf("scm-state(%d)", uint32(s))
	}
}

const defaultSCMPoll = 100 * time.Millisecond

func scmWaitTimeout(timeout time.Duration) time.Duration {
	if timeout > 0 {
		return timeout
	}
	return 30 * time.Second
}
