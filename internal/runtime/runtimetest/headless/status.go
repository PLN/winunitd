package headless

import (
	"errors"

	"github.com/PLN/winunitd/internal/protocol"
)

// StatusFromSnapshot is a unit's status proof from its manager's snapshot.
func StatusFromSnapshot(snap *protocol.SnapshotResult, unit string, at uint64) (UnitStatusProof, error) {
	if snap == nil {
		return UnitStatusProof{}, errors.New("no snapshot")
	}
	for _, u := range snap.Units {
		if u.Name != unit {
			continue
		}
		p := UnitStatusProof{Unit: u.Name, ActiveState: u.ActiveState, Reason: u.Reason, RestartAttempt: u.RestartAttempt, At: at}
		if b := u.RestartBudget; b != nil {
			p.Budget = &StatusBudget{Policy: b.Policy, IntervalSec: b.IntervalSec, Burst: b.Burst, Remaining: b.Remaining}
		}
		return p, nil
	}
	return UnitStatusProof{}, errors.New("the snapshot has no such unit")
}
