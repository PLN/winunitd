package core

import (
	"errors"
	"testing"
)

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
	if SubWatchdog.String() != "watchdog" {
		t.Fatalf("watchdog = %q", SubWatchdog.String())
	}
	if SubNone.String() != "" {
		t.Fatalf("none = %q", SubNone.String())
	}
}

func TestStepAllowed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		from    State
		fromSub Substate
		ev      Event
		want    State
		wantSub Substate
	}{
		{Inactive, SubNone, EventStartRequested, Activating, SubStart},
		{Failed, SubNone, EventStartRequested, Activating, SubStart},
		{Failed, SubWatchdog, EventStartRequested, Activating, SubStart},
		{Activating, SubAutoRestart, EventStartRequested, Activating, SubStart},
		{Activating, SubStart, EventStartSucceeded, Active, SubRunning},
		{Activating, SubAutoRestart, EventStartSucceeded, Active, SubRunning},
		{Activating, SubStart, EventStartFailed, Failed, SubNone},
		{Active, SubRunning, EventStartFailed, Failed, SubNone},
		{Deactivating, SubStop, EventStartFailed, Failed, SubNone},
		{Active, SubRunning, EventMainExited, Failed, SubNone},
		{Activating, SubStart, EventMainExited, Failed, SubNone},
		{Inactive, SubNone, EventMainExited, Failed, SubNone},
		{Active, SubRunning, EventAutoRestart, Activating, SubAutoRestart},
		{Failed, SubNone, EventAutoRestart, Activating, SubAutoRestart},
		{Failed, SubWatchdog, EventAutoRestart, Activating, SubAutoRestart},
		{Active, SubRunning, EventStopRequested, Deactivating, SubStop},
		{Activating, SubStart, EventStopRequested, Deactivating, SubStop},
		{Failed, SubNone, EventStopRequested, Deactivating, SubStop},
		{Inactive, SubNone, EventStopRequested, Deactivating, SubStop},
		{Deactivating, SubStop, EventStopFinished, Inactive, SubNone},
		{Activating, SubAutoRestart, EventRestartCancelled, Inactive, SubNone},
		{Active, SubRunning, EventWatchdogFailed, Failed, SubWatchdog},
		{Activating, SubStart, EventWatchdogFailed, Failed, SubWatchdog},
		{Inactive, SubNone, EventAutoRestart, Activating, SubAutoRestart},
	}
	for _, tt := range tests {
		got, sub, err := Step(tt.from, tt.fromSub, tt.ev)
		if err != nil {
			t.Errorf("Step(%s, %s, %s) error = %v, want allowed (%s, %s)",
				tt.from, tt.fromSub, tt.ev, err, tt.want, tt.wantSub)
			continue
		}
		if got != tt.want || sub != tt.wantSub {
			t.Errorf("Step(%s, %s, %s) = (%s, %s), want (%s, %s)",
				tt.from, tt.fromSub, tt.ev, got, sub, tt.want, tt.wantSub)
		}
	}
}

func TestStepRejected(t *testing.T) {
	t.Parallel()
	// These used to silently accept any from-state (event-only switch).
	tests := []struct {
		from    State
		fromSub Substate
		ev      Event
	}{
		{Activating, SubStart, EventStopFinished},
		{Activating, SubAutoRestart, EventStopFinished},
		{Active, SubRunning, EventStopFinished},
		{Inactive, SubNone, EventStopFinished},
		{Failed, SubNone, EventStopFinished},
		{Failed, SubNone, EventStartSucceeded},
		{Failed, SubWatchdog, EventStartSucceeded},
		{Inactive, SubNone, EventStartSucceeded},
		{Active, SubRunning, EventStartSucceeded},
		{Deactivating, SubStop, EventStartSucceeded},
		{Inactive, SubNone, EventWatchdogFailed},
		{Failed, SubNone, EventWatchdogFailed},
		{Deactivating, SubStop, EventWatchdogFailed},
		{Deactivating, SubStop, EventStartRequested},
		{Activating, SubStart, EventAutoRestart},
		{Deactivating, SubStop, EventAutoRestart},
		{Active, SubRunning, EventRestartCancelled},
		{Activating, SubStart, EventRestartCancelled},
		{Inactive, SubNone, EventRestartCancelled},
		{Failed, SubNone, EventMainExited},
		{Deactivating, SubStop, EventMainExited},
		{Inactive, SubNone, EventStartFailed},
		{Failed, SubNone, EventStartFailed},
		{Active, SubRunning, Event(99)},
		{Activating, SubStart, Event(-1)},
	}
	for _, tt := range tests {
		got, sub, err := Step(tt.from, tt.fromSub, tt.ev)
		if err == nil {
			t.Errorf("Step(%s, %s, %s) = (%s, %s), want rejection",
				tt.from, tt.fromSub, tt.ev, got, sub)
			continue
		}
		if !errors.Is(err, ErrIllegalTransition) {
			t.Errorf("Step(%s, %s, %s) error = %v, want ErrIllegalTransition",
				tt.from, tt.fromSub, tt.ev, err)
		}
		if got != tt.from || sub != tt.fromSub {
			t.Errorf("Step(%s, %s, %s) rejected but mutated to (%s, %s)",
				tt.from, tt.fromSub, tt.ev, got, sub)
		}
	}
}

func TestStepFromStateIsNotDecorative(t *testing.T) {
	t.Parallel()
	// Same event, different from: one allowed, one rejected.
	if _, _, err := Step(Deactivating, SubStop, EventStopFinished); err != nil {
		t.Fatalf("Deactivating + stop-finished must be allowed: %v", err)
	}
	if _, _, err := Step(Activating, SubStart, EventStopFinished); err == nil {
		t.Fatal("Activating + stop-finished must be rejected")
	}
	if _, _, err := Step(Activating, SubStart, EventStartSucceeded); err != nil {
		t.Fatalf("Activating + start-succeeded must be allowed: %v", err)
	}
	if _, _, err := Step(Failed, SubNone, EventStartSucceeded); err == nil {
		t.Fatal("Failed + start-succeeded must be rejected")
	}
	if _, _, err := Step(Active, SubRunning, EventWatchdogFailed); err != nil {
		t.Fatalf("Active + watchdog-failed must be allowed: %v", err)
	}
	if _, _, err := Step(Inactive, SubNone, EventWatchdogFailed); err == nil {
		t.Fatal("Inactive + watchdog-failed must be rejected")
	}
}
