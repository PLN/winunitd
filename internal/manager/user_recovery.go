package manager

import (
	"time"

	"github.com/PLN/winunitd/internal/protocol"
)

// RecoveryStatus copies decisions without querying native process handles.
// Records share the tracked-instance cap and are removed by explicit cleanup.
func (h *UserHost) RecoveryStatus() []protocol.UserRecoveryStatus {
	return h.decisionSnapshot().recovery
}

// Linger has a known SID before native token acquisition. Retain failures in
// the same bounded record budget, without overwriting a concurrent launch.
func (h *UserHost) recordLingerTokenFailure(sid string, err error) {
	unlock, ok := h.ops.tryLock(sid)
	if !ok {
		return
	}
	defer unlock()
	now := h.cfg.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || !h.lingeringLocked(sid) {
		return
	}
	previous := h.bySID[sid]
	if previous == nil && len(h.bySID) >= maxTrackedUserManagers {
		return
	}
	if previous != nil && (previous.proc != nil || previous.err == "") {
		return
	}
	delay := userRecoveryDelay(previous, now)
	h.bySID[sid] = &userInstance{sid: sid, err: err.Error(), restartDelay: delay, nextStart: now.Add(delay)}
}

func userRecoveryDelay(previous *userInstance, now time.Time) time.Duration {
	if previous == nil || (!previous.startedAt.IsZero() && now.Sub(previous.startedAt) >= userRecoveryStableTime) {
		return userRecoveryMinDelay
	}
	delay := previous.restartDelay * 2
	if delay < userRecoveryMinDelay {
		return userRecoveryMinDelay
	}
	if delay > userRecoveryMaxDelay {
		return userRecoveryMaxDelay
	}
	return delay
}
