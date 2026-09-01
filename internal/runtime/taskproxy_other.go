//go:build !windows

package runtime

import (
	"context"
	"fmt"
	"time"
)

// errTaskNotWindows is returned by the Type=scheduled-task stub on
// non-Windows builds. Tests inject a fake TaskScheduler; production
// Linux never talks to a real Task Scheduler.
var errTaskNotWindows = fmt.Errorf("Type=scheduled-task is only available on Windows")

type stubTasks struct{}

// DefaultTaskScheduler returns a stub that does not call a real Task Scheduler.
func DefaultTaskScheduler() TaskScheduler {
	return stubTasks{}
}

func (stubTasks) Start(context.Context, string, time.Duration) (TaskStatus, error) {
	return TaskStatus{}, errTaskNotWindows
}

func (stubTasks) Stop(context.Context, string, time.Duration) (TaskStatus, error) {
	return TaskStatus{}, errTaskNotWindows
}

func (stubTasks) Query(string) (TaskStatus, error) {
	return TaskStatus{}, errTaskNotWindows
}
