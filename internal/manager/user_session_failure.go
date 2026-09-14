package manager

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/PLN/winunitd/internal/protocol"
)

const (
	maxSnapshotSessionFailures = 128
	maxSessionFailureBytes     = 256
)

// Session observations have the same lifetime and bound as native session
// enumeration. A failed lookup must not invent an admitted SID or manager.
type userSessionObservation struct {
	failure protocol.UserSessionFailure
}

func (h *UserHost) recordSessionFailure(origin userSessionOrigin, sid, stage string, err error) {
	// Error rendering can call external code; keep it outside the decision lock.
	message := strings.ToValidUTF8(err.Error(), "\uFFFD")
	if len(message) > maxSessionFailureBytes {
		end := maxSessionFailureBytes - len("...")
		for !utf8.RuneStart(message[end]) {
			end--
		}
		message = message[:end] + "..."
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || !origin.currentLocked(h) {
		return
	}
	if _, observed := h.sessionObservations[origin.session]; !observed {
		return
	}
	h.sessionObservations[origin.session] = userSessionObservation{failure: protocol.UserSessionFailure{
		SessionID: origin.session, SID: sid, Stage: stage, Error: message,
	}}
}

func (h *UserHost) clearSessionFailure(origin userSessionOrigin) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || !origin.currentLocked(h) {
		return
	}
	if _, observed := h.sessionObservations[origin.session]; observed {
		h.sessionObservations[origin.session] = userSessionObservation{}
	}
}

// Caller holds h.mu. Detail truncation is explicit and deterministic; all
// admitted session observations remain retained for recovery and removal.
func (h *UserHost) sessionFailuresLocked() ([]protocol.UserSessionFailure, int) {
	var ids []uint32
	for id, observation := range h.sessionObservations {
		if observation.failure.Stage != "" {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	count := min(len(ids), maxSnapshotSessionFailures)
	var result []protocol.UserSessionFailure
	for _, id := range ids[:count] {
		result = append(result, h.sessionObservations[id].failure)
	}
	return result, len(ids) - count
}
