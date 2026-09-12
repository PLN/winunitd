package manager

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/PLN/winunitd/internal/protocol"
)

const (
	maxSnapshotUnits      = 1024
	maxSnapshotOperations = 128
	maxSnapshotBytes      = 512 << 10
)

// Snapshot copies the entire accepted view while holding the decision mutex.
// Nothing in the result aliases a mutable runtime record. Native observations
// cannot overwrite it after publication, and no worker or I/O is dispatched.
func (m *Manager) Snapshot() (*protocol.SnapshotResult, error) {
	m.mu.Lock()
	result, err := m.snapshotLocked()
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	// Enforce the transport allowance outside the coordinator. Record-count
	// admission bounds the copy; reject oversized text rather than truncating
	// units or returning a partial view that could be mistaken for complete.
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxSnapshotBytes {
		return nil, protocol.ErrFailed("coordinator snapshot exceeds 512 KiB; use individual status/operation queries")
	}
	return result, nil
}

func (m *Manager) snapshotLocked() (*protocol.SnapshotResult, error) {
	if len(m.units) > maxSnapshotUnits || len(m.activeOperations) > maxSnapshotOperations {
		return nil, protocol.ErrFailed("coordinator snapshot exceeds 1024 units or 128 active operations")
	}
	m.snapshotSequence++
	out := &protocol.SnapshotResult{
		ManagerID: m.configNamespace, Sequence: m.snapshotSequence,
		CapturedAt: m.now().UTC().Format(time.RFC3339Nano),
		Machine:    *m.machineLocked(),
		Units:      make([]protocol.UnitSnapshot, 0, len(m.units)),
		Operations: make([]protocol.OperationResult, 0, len(m.activeOperations)),
	}
	if m.closed {
		out.Machine.State = "closing"
	}
	for _, name := range m.names() {
		st := m.unitStatusLocked(name)
		out.Units = append(out.Units, protocol.UnitSnapshot{
			Name: st.Name, LoadState: st.LoadState, ActiveState: st.ActiveState,
			SubState: st.SubState, Enabled: st.Enabled, MainPID: st.MainPID,
			InvocationID: st.InvocationID, ConfigRevision: st.ConfigRevision,
			InvocationConfigRevision: st.InvocationConfigRevision,
			ArmedConfigRevision:      st.ArmedConfigRevision, LastOperationID: st.LastOperationID,
			PendingCleanup: st.PendingCleanup,
		})
	}
	for _, operation := range m.operations {
		if operation.State == "running" {
			if len(out.Operations) == maxSnapshotOperations {
				return nil, protocol.ErrFailed("coordinator snapshot exceeds 128 active operations")
			}
			out.Operations = append(out.Operations, *operation)
		}
	}
	sort.Slice(out.Operations, func(i, j int) bool { return out.Operations[i].ID < out.Operations[j].ID })
	return out, nil
}
