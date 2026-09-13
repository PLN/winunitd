package manager

import (
	"slices"

	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
)

// Caller holds h.mu; copying a system snapshot also holds the manager lock.
func (h *UserHost) snapshotLocked() (*protocol.UserHostSnapshot, error) {
	if len(h.bySID) > maxTrackedUserManagers || len(h.lingerRecords) > maxTrackedUserManagers || len(h.sessions) > runtime.MaxInteractiveSessions || len(h.sessionRequests) > runtime.MaxInteractiveSessions || len(h.nativeWork) > maxNativeUserWork+3 {
		return nil, protocol.ErrFailed("user-host snapshot exceeds admitted record bounds")
	}
	view := h.decisionSnapshotLocked()
	out := &protocol.UserHostSnapshot{
		HostID: h.hostID, State: "running", AdmissionRevision: h.admissionRevision, LingerRevision: h.lingerRevision, SessionEpoch: h.nextSessionRequest,
		NativeWork: view.native, Lingering: view.lingering, LingerState: view.lingerState, LingerError: view.lingerError, Instances: view.instances, Recovery: view.recovery,
	}
	if h.closed {
		out.State = "closing"
	}
	for work := range h.nativeWork {
		if work.token != nil {
			out.PendingTokenCleanup++
		}
	}
	ids := make(map[uint32]struct{}, len(h.sessions)+len(h.sessionRequests))
	for id := range h.sessions {
		ids[id] = struct{}{}
	}
	for id := range h.sessionRequests {
		ids[id] = struct{}{}
	}
	for id := range ids {
		out.Sessions = append(out.Sessions, protocol.UserSessionSnapshot{SessionID: id, SID: h.sessions[id], PendingRequest: h.sessionRequests[id]})
	}
	slices.SortFunc(out.Sessions, func(a, b protocol.UserSessionSnapshot) int {
		if a.SessionID < b.SessionID {
			return -1
		}
		if a.SessionID > b.SessionID {
			return 1
		}
		return 0
	})
	return out, nil
}
