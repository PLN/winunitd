package runtime

import "testing"

func TestExitStatus(t *testing.T) {
	t.Parallel()
	e := &ExitStatus{Code: 2}
	if e.Error() != "exit status 2" {
		t.Fatalf("error = %q", e.Error())
	}
	if !e.Failed() {
		t.Fatal("nonzero should be failed")
	}
	if (&ExitStatus{Code: 0}).Failed() {
		t.Fatal("zero should not be failed")
	}
	if (*ExitStatus)(nil).Failed() {
		t.Fatal("nil should not be failed")
	}
}

func TestExitStatusSignalEquivalent(t *testing.T) {
	t.Parallel()
	const statusAccessViolation = uint32(0xC0000005)
	if !(&ExitStatus{Code: statusAccessViolation}).SignalEquivalent() {
		t.Fatal("0xC0000005 is signal-equivalent")
	}
	if !(&ExitStatus{Code: ntstatusErrorSeverity}).SignalEquivalent() {
		t.Fatal("0xC0000000 is signal-equivalent")
	}
	if (&ExitStatus{Code: ntstatusErrorSeverity - 1}).SignalEquivalent() {
		t.Fatal("0xBFFFFFFF is not signal-equivalent")
	}
	if (&ExitStatus{Code: 2}).SignalEquivalent() {
		t.Fatal("ordinary exit 2 is not signal-equivalent")
	}
	if (&ExitStatus{Code: 0}).SignalEquivalent() {
		t.Fatal("exit 0 is not signal-equivalent")
	}
	if (*ExitStatus)(nil).SignalEquivalent() {
		t.Fatal("nil is not signal-equivalent")
	}
	e := &ExitStatus{Code: statusAccessViolation}
	if got := e.Error(); got != "signal-equivalent: exit status 3221225477" {
		t.Fatalf("Error = %q", got)
	}
}
