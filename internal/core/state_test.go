package core

import "testing"

func TestStateString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		s    State
		want string
	}{
		{Inactive, "inactive"},
		{Activating, "activating"},
		{Active, "active"},
		{Deactivating, "deactivating"},
		{Failed, "failed"},
	}
	for _, tt := range tests {
		if got := tt.s.String(); got != tt.want {
			t.Errorf("%d.String() = %q, want %q", tt.s, got, tt.want)
		}
	}
}

func TestSubstateString(t *testing.T) {
	t.Parallel()
	if SubAutoRestart.String() != "auto-restart" {
		t.Fatalf("auto-restart = %q", SubAutoRestart.String())
	}
	if SubRunning.String() != "running" {
		t.Fatalf("running = %q", SubRunning.String())
	}
	if SubNone.String() != "" {
		t.Fatalf("none = %q", SubNone.String())
	}
}

func TestStep(t *testing.T) {
	t.Parallel()
	tests := []struct {
		from    State
		fromSub Substate
		ev      Event
		want    State
		wantSub Substate
	}{
		{Inactive, SubNone, EventStartRequested, Activating, SubStart},
		{Activating, SubStart, EventStartSucceeded, Active, SubRunning},
		{Activating, SubStart, EventStartFailed, Failed, SubNone},
		{Active, SubRunning, EventMainExited, Failed, SubNone},
		{Active, SubRunning, EventAutoRestart, Activating, SubAutoRestart},
		{Failed, SubNone, EventAutoRestart, Activating, SubAutoRestart},
		{Activating, SubAutoRestart, EventStartSucceeded, Active, SubRunning},
		{Active, SubRunning, EventStopRequested, Deactivating, SubStop},
		{Deactivating, SubStop, EventStopFinished, Inactive, SubNone},
		{Activating, SubAutoRestart, EventRestartCancelled, Inactive, SubNone},
		{Activating, SubStart, EventMainExited, Failed, SubNone},
		{Deactivating, SubStop, EventStartFailed, Failed, SubNone},
	}
	for _, tt := range tests {
		got, sub := Step(tt.from, tt.fromSub, tt.ev)
		if got != tt.want || sub != tt.wantSub {
			t.Errorf("Step(%s, %s, %s) = (%s, %s), want (%s, %s)",
				tt.from, tt.fromSub, tt.ev, got, sub, tt.want, tt.wantSub)
		}
	}
}
