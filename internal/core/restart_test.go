package core

import (
	"testing"

	"github.com/PLN/winunitd/internal/unit"
)

func TestShouldRestart(t *testing.T) {
	t.Parallel()
	tests := []struct {
		policy unit.RestartPolicy
		kind   ExitKind
		want   bool
	}{
		{unit.RestartNo, ExitSuccess, false},
		{unit.RestartNo, ExitFailure, false},
		{unit.RestartNo, ExitAbnormal, false},
		{unit.RestartAlways, ExitSuccess, true},
		{unit.RestartAlways, ExitFailure, true},
		{unit.RestartAlways, ExitAbnormal, true},
		{unit.RestartOnFailure, ExitSuccess, false},
		{unit.RestartOnFailure, ExitFailure, true},
		{unit.RestartOnFailure, ExitAbnormal, true},
		{unit.RestartOnFailure, ExitWatchdog, true},
		{unit.RestartOnFailure, ExitResourceLimit, true},
		{unit.RestartAlways, ExitResourceLimit, true},
		{unit.RestartOnWatchdog, ExitResourceLimit, false},
		{unit.RestartNo, ExitResourceLimit, false},
		{unit.RestartOnWatchdog, ExitSuccess, false},
		{unit.RestartOnWatchdog, ExitFailure, false},
		{unit.RestartOnWatchdog, ExitWatchdog, true},
		{unit.RestartAlways, ExitWatchdog, true},
		{unit.RestartNo, ExitWatchdog, false},
		{"", ExitFailure, false},
	}
	for _, tt := range tests {
		got := ShouldRestart(tt.policy, tt.kind)
		if got != tt.want {
			t.Errorf("ShouldRestart(%q, %s) = %v, want %v", tt.policy, tt.kind, got, tt.want)
		}
	}
}

func TestStatusReason(t *testing.T) {
	t.Parallel()
	if StatusReason(ReasonResourceLimit) != ReasonResourceLimit {
		t.Fatal("resource-limit")
	}
	if StatusReason(ReasonSignalEquivalent) != ReasonSignalEquivalent {
		t.Fatal("signal-equivalent")
	}
	if StatusReason(ReasonStartLimit) != ReasonStartLimit {
		t.Fatal("start-limit")
	}
	if StatusReason(ReasonConfiguration) != ReasonConfiguration {
		t.Fatal("configuration")
	}
	if StatusReason("configuration: registry key is missing") != ReasonConfiguration {
		t.Fatal("configuration prefix")
	}
	if StatusReason("main process exited") != "" {
		t.Fatal("other errors have no reason")
	}
	if StatusReason("start-limit-hit") != "" {
		t.Fatal("start-limit-hit is not the status reason token")
	}
}
