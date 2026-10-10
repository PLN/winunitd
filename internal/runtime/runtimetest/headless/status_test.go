package headless

import (
	"testing"

	"github.com/PLN/winunitd/internal/protocol"
)

func TestStatusFromSnapshot(t *testing.T) {
	remaining := 0
	snap := &protocol.SnapshotResult{Units: []protocol.UnitSnapshot{
		{Name: "serve.service", ActiveState: "active"},
		{Name: "finite.service", ActiveState: "failed", Reason: "start-limit", RestartAttempt: 4,
			RestartBudget: &protocol.RestartBudget{Policy: "always", IntervalSec: 10, Burst: 5, Remaining: &remaining}},
	}}
	p, err := StatusFromSnapshot(snap, "finite.service", ft(1))
	if err != nil || p.Reason != "start-limit" || p.Budget == nil || p.Budget.Burst != 5 || *p.Budget.Remaining != 0 || p.At != ft(1) {
		t.Fatalf("status %+v %v", p, err)
	}
	if m := metrics(Lifecycle{}, &p, 0, 0); m["startLimited"] != 1 || m["unlimited"] != 0 {
		t.Fatalf("metrics %v", m)
	}
	if _, err := StatusFromSnapshot(snap, "missing.service", 0); err == nil {
		t.Fatal("a missing unit produced a status")
	}
}
