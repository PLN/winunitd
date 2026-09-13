package manager

import (
	"fmt"
	"maps"
	"reflect"

	"github.com/PLN/winunitd/internal/runtime"
)

// Session observations carry both request identity and accepted policy revision.
// The worker may acquire a token/probe files only outside the decision lock.
type userSessionOrigin struct {
	session           uint32
	request, revision uint64
}

// Caller holds h.mu; this predicate performs no external observation.
func (o userSessionOrigin) currentLocked(h *UserHost) bool {
	return h.sessionRequests[o.session] == o.request && h.admissionRevision == o.revision
}

func (h *UserHost) acceptSessionRequest(sessionID uint32) (uint64, *userNativeWork, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return 0, nil, nil
	}
	if _, known := h.sessions[sessionID]; !known && len(h.sessions) >= runtime.MaxInteractiveSessions {
		return 0, nil, fmt.Errorf("session admission exceeds limit %d", runtime.MaxInteractiveSessions)
	}
	if _, known := h.sessionRequests[sessionID]; !known && len(h.sessionRequests) >= runtime.MaxInteractiveSessions {
		return 0, nil, fmt.Errorf("pending session admission exceeds limit %d", runtime.MaxInteractiveSessions)
	}
	h.nextSessionRequest++
	request := h.nextSessionRequest
	work, err := h.acceptNativeUserWorkLocked()
	if err != nil {
		delete(h.sessionRequests, sessionID)
		return 0, nil, fmt.Errorf("session %d admission: %w", sessionID, err)
	}
	h.sessionRequests[sessionID] = request
	return request, work, nil
}

func (h *UserHost) finishSessionRequest(sessionID uint32, request uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sessionRequests[sessionID] == request {
		delete(h.sessionRequests, sessionID)
	}
}

func (h *UserHost) acceptSessionIdentity(origin userSessionOrigin, sid string) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || !origin.currentLocked(h) {
		return false, nil
	}
	if _, known := h.sessions[origin.session]; !known && len(h.sessions) >= runtime.MaxInteractiveSessions {
		return false, fmt.Errorf("session admission exceeds limit %d", runtime.MaxInteractiveSessions)
	}
	h.sessions[origin.session] = sid
	return true, nil
}

func (h *UserHost) recordLogoff(sessionID uint32) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextSessionRequest++ // invalidate an enumeration predating this logoff
	sid := h.sessions[sessionID]
	delete(h.sessionRequests, sessionID)
	delete(h.sessions, sessionID)
	return sid
}

// p is a validated private copy. Revocation effects recheck the latest policy
// after acquiring the SID gate, so an obsolete selection cannot kill a new owner.
func (h *UserHost) acceptAdmissionPolicy(p UserAdmission) ([]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, fmt.Errorf("user host is closed")
	}
	if !reflect.DeepEqual(p, h.admission) {
		h.admission = p
		h.admissionRevision++
	}
	var revoked []string
	for sid := range h.bySID {
		allow, probe := p.decision(sid)
		if !allow && !probe && !h.lingeringLocked(sid) {
			revoked = append(revoked, sid)
		}
	}
	return revoked, nil
}

// Caller owns the SID gate. Returned instance identity remains the cleanup owner.
func (h *UserHost) acceptAdmissionRevocation(sid string) (*userInstance, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	allow, probe := h.admission.decision(sid)
	inst := h.bySID[sid]
	revoke := !allow && !probe && !h.lingeringLocked(sid)
	if revoke {
		for session, mapped := range h.sessions {
			if mapped == sid {
				delete(h.sessions, session)
			}
		}
	}
	return inst, revoke
}

// Caller owns lingerIO and the accepted native work until persistence publication.
// An already accepted mutation must still describe its completed disk effect if
// shutdown began during I/O; closure separately prevents a new launch.
func (h *UserHost) acceptLingerMutation(rec runtime.LingerRecord, enable bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lingerRevision++
	if enable {
		h.lingerRecords[rec.SID] = rec
	} else {
		delete(h.lingerRecords, rec.SID)
	}
}

// Caller owns lingerIO. accepted is a private validated map transferred into
// policy ownership. Rendering observation errors and all I/O precede this handler.
func (h *UserHost) acceptLingerSnapshot(accepted map[string]runtime.LingerRecord, scanError string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var removed []string
	if !h.closed {
		for sid := range h.lingerRecords {
			if _, present := accepted[sid]; !present {
				removed = append(removed, sid)
			}
		}
		if !maps.Equal(h.lingerRecords, accepted) {
			h.lingerRevision++
		}
		h.lingerRecords = accepted
		h.lingerKnown = true
		h.lingerError = scanError
	}
	return removed
}
