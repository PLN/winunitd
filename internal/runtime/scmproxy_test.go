package runtime

import (
	"testing"
	"time"
)

func TestSCMStatusActiveState(t *testing.T) {
	t.Parallel()
	cases := []struct {
		state SCMState
		want  string
	}{
		{SCMRunning, "active"},
		{SCMStopped, "inactive"},
		{SCMPaused, "inactive"},
		{SCMStartPending, "activating"},
		{SCMContinuePending, "activating"},
		{SCMStopPending, "deactivating"},
		{SCMPausePending, "deactivating"},
		{0, "failed"},
		{99, "failed"},
	}
	for _, tc := range cases {
		got := SCMStatus{State: tc.state}.ActiveState()
		if got != tc.want {
			t.Fatalf("state %s ActiveState = %q, want %q", tc.state, got, tc.want)
		}
	}
}

func TestSCMWaitTimeoutDefault(t *testing.T) {
	t.Parallel()
	if scmWaitTimeout(0) != 30*time.Second {
		t.Fatalf("zero timeout default = %s", scmWaitTimeout(0))
	}
	if scmWaitTimeout(5*time.Second) != 5*time.Second {
		t.Fatalf("explicit timeout = %s", scmWaitTimeout(5*time.Second))
	}
}
