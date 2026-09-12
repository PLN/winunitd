package manager

import (
	"fmt"

	"github.com/PLN/winunitd/internal/runtime"
)

// acceptUserSessions publishes one authoritative enumeration. Reserving logon
// requests in the same decision prevents a later logoff from being overwritten
// when the enumeration worker eventually reaches that session's token lookup.
func (h *UserHost) acceptUserSessions(ids []uint32, epoch, revision uint64) (map[uint32]uint64, []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.nextSessionRequest != epoch || h.admissionRevision != revision {
		return nil, nil
	}
	h.nextSessionRequest++ // even an empty snapshot supersedes older enumerations
	requests := make(map[uint32]uint64, len(ids))
	for _, id := range ids {
		if _, duplicate := requests[id]; duplicate {
			continue
		}
		h.nextSessionRequest++
		requests[id] = h.nextSessionRequest
		h.sessionRequests[id] = h.nextSessionRequest
	}
	for id := range h.sessions {
		if _, present := requests[id]; !present {
			delete(h.sessions, id)
		}
	}
	for id := range h.sessionRequests {
		if _, present := requests[id]; !present {
			delete(h.sessionRequests, id)
		}
	}
	// Include retained failed cleanup so a later pass retries it even though
	// the original session mapping was already removed.
	sids := make([]string, 0, len(h.bySID))
	for sid := range h.bySID {
		sids = append(sids, sid)
	}
	return requests, sids
}

// User-host instance decisions use h.mu and retained instance identity. The SID
// gate serializes workers; token lookup, liveness, launch and kill stay outside
// these decisions. Session/admission policy remains a separate migration row.
func (h *UserHost) inspectUserLaunch(sid string, wanted func() bool) (*userInstance, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || !wanted() {
		return nil, fmt.Errorf("user manager launch is no longer requested")
	}
	inst := h.bySID[sid]
	if inst != nil && inst.uncertain {
		return nil, fmt.Errorf("user manager termination is unconfirmed; retry cleanup")
	}
	return inst, nil
}

func (h *UserHost) acceptUserLaunch(sid string, wanted func() bool) (*userInstance, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || !wanted() {
		return nil, fmt.Errorf("user manager launch is no longer requested")
	}
	inst := &userInstance{sid: sid}
	h.bySID[sid] = inst // shutdown retains accepted work even before process creation
	return inst, nil
}

func (h *UserHost) applyUserLaunch(sid string, inst *userInstance, proc runtime.UserManagerProc, err error, wanted func() bool) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	inst.proc = proc // even a failed or superseded creation remains owned
	superseded := h.closed || h.bySID[sid] != inst || !wanted()
	if proc == nil {
		if h.bySID[sid] == inst {
			delete(h.bySID, sid)
		}
	} else if err != nil {
		inst.uncertain = true
		inst.err = err.Error()
	}
	return superseded
}

func (h *UserHost) acceptUserCleanup(inst *userInstance) runtime.UserManagerProc {
	h.mu.Lock()
	defer h.mu.Unlock()
	inst.uncertain = true
	return inst.proc
}

func (h *UserHost) applyUserCleanup(sid string, inst *userInstance, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.bySID[sid] != inst {
		return
	}
	if err == nil {
		delete(h.bySID, sid)
	} else {
		inst.err = err.Error()
	}
}

func (h *UserHost) acceptUserShutdown() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sealShutdownLocked()
	sids := make([]string, 0, len(h.bySID))
	for sid := range h.bySID {
		sids = append(sids, sid)
	}
	return sids
}

// Caller holds h.mu. No token, profile, filesystem or process work occurs here.
func (h *UserHost) sealShutdownLocked() {
	h.closed = true
	h.sessions = make(map[uint32]string)
	h.sessionRequests = make(map[uint32]uint64)
}
