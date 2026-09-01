package runtime

import (
	"testing"
	"time"
)

func TestTaskStatusActiveState(t *testing.T) {
	t.Parallel()
	cases := []struct {
		status TaskStatus
		want   string
	}{
		{TaskStatus{State: TaskRunning}, "active"},
		{TaskStatus{State: TaskReady, Instances: 1, PID: 9}, "active"},
		{TaskStatus{State: TaskQueued}, "activating"},
		{TaskStatus{State: TaskReady}, "inactive"},
		{TaskStatus{State: TaskDisabled}, "inactive"},
		{TaskStatus{State: TaskUnknown}, "failed"},
		{TaskStatus{State: 99}, "failed"},
	}
	for _, tc := range cases {
		got := tc.status.ActiveState()
		if got != tc.want {
			t.Fatalf("status %+v ActiveState = %q, want %q", tc.status, got, tc.want)
		}
	}
}

func TestTaskWaitTimeoutDefault(t *testing.T) {
	t.Parallel()
	if taskWaitTimeout(0) != 30*time.Second {
		t.Fatalf("zero timeout default = %s", taskWaitTimeout(0))
	}
	if taskWaitTimeout(5*time.Second) != 5*time.Second {
		t.Fatalf("explicit timeout = %s", taskWaitTimeout(5*time.Second))
	}
}

func TestTaskStateString(t *testing.T) {
	t.Parallel()
	if TaskReady.String() != "ready" {
		t.Fatalf("ready = %s", TaskReady)
	}
	if TaskRunning.String() != "running" {
		t.Fatalf("running = %s", TaskRunning)
	}
}
