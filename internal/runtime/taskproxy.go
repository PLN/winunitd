package runtime

import (
	"context"
	"fmt"
	"time"
)

// TaskScheduler orchestrates an existing registered Windows Task Scheduler
// task (DESIGN.md §52). Implementations must not create, change, delete,
// enable, or disable the task definition.
type TaskScheduler interface {
	Start(ctx context.Context, name string, timeout time.Duration) (TaskStatus, error)
	Stop(ctx context.Context, name string, timeout time.Duration) (TaskStatus, error)
	Query(name string) (TaskStatus, error)
}

// TaskState is IRegisteredTask.State (TASK_STATE_*).
type TaskState uint32

const (
	TaskUnknown  TaskState = 0
	TaskDisabled TaskState = 1
	TaskQueued   TaskState = 2
	TaskReady    TaskState = 3
	TaskRunning  TaskState = 4
)

// TaskStatus is Task Scheduler task/instance state mapped into winunitd terms.
type TaskStatus struct {
	State     TaskState
	Instances int
	PID       int
}

// ActiveState maps Task Scheduler state onto existing ActiveState strings
// (inactive / activating / active / failed). A running instance is active
// even if State has not yet updated to TASK_STATE_RUNNING.
func (s TaskStatus) ActiveState() string {
	if s.Instances > 0 || s.State == TaskRunning {
		return "active"
	}
	switch s.State {
	case TaskQueued:
		return "activating"
	case TaskReady, TaskDisabled:
		return "inactive"
	default:
		return "failed"
	}
}

func (s TaskState) String() string {
	switch s {
	case TaskUnknown:
		return "unknown"
	case TaskDisabled:
		return "disabled"
	case TaskQueued:
		return "queued"
	case TaskReady:
		return "ready"
	case TaskRunning:
		return "running"
	default:
		return fmt.Sprintf("task-state(%d)", uint32(s))
	}
}

func taskWaitTimeout(timeout time.Duration) time.Duration {
	if timeout > 0 {
		return timeout
	}
	return 30 * time.Second
}
