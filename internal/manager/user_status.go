package manager

import (
	"slices"
	"strings"
	"time"

	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
)

func userLaunchMode(token *runtime.UserToken) (string, uint32) {
	if token == nil {
		return "", 0
	}
	switch token.Source {
	case runtime.LingerTokenPathS4U:
		return "headless-s4u", 0
	case runtime.LingerTokenPathStoreURI:
		return "headless-store-uri", 0
	case "":
		return "interactive", token.SessionID
	default:
		return "unknown", token.SessionID
	}
}

type userDecisionSnapshot struct {
	running, native int
	instances       []protocol.UserManagerStatus
	recovery        []protocol.UserRecoveryStatus
}

// Copy one bounded decision view without filesystem or process-handle queries.
// A running decision is revised by reconciliation after an observed process exit.
func (h *UserHost) decisionSnapshot() userDecisionSnapshot {
	var result userDecisionSnapshot
	if h == nil {
		return result
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	result.native = len(h.nativeWork)
	sessions := make(map[string]int)
	for _, sid := range h.sessions {
		sessions[sid]++
	}
	for sid, inst := range h.bySID {
		state := "starting"
		switch {
		case inst.uncertain:
			state = "cleanup"
		case inst.proc != nil:
			state = "running"
			result.running++
		case inst.err != "":
			state = "waiting"
		}
		result.instances = append(result.instances, protocol.UserManagerStatus{SID: sid, Mode: inst.mode, SessionID: inst.session, PID: inst.pid, State: state, InteractiveSessions: sessions[sid]})
		if state != "running" {
			r := protocol.UserRecoveryStatus{SID: sid, State: state, Error: inst.err}
			if state == "waiting" && !inst.nextStart.IsZero() {
				r.NextAttemptAt = inst.nextStart.UTC().Format(time.RFC3339Nano)
			}
			result.recovery = append(result.recovery, r)
		}
	}
	slices.SortFunc(result.instances, func(a, b protocol.UserManagerStatus) int { return strings.Compare(a.SID, b.SID) })
	slices.SortFunc(result.recovery, func(a, b protocol.UserRecoveryStatus) int { return strings.Compare(a.SID, b.SID) })
	return result
}
