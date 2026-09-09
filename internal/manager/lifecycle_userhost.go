package manager

import (
	"fmt"

	"github.com/PLN/winunitd/internal/runtime"
)

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
	h.closed = true
	sids := make([]string, 0, len(h.bySID))
	for sid := range h.bySID {
		sids = append(sids, sid)
	}
	h.sessions = make(map[uint32]string)
	h.sessionRequests = make(map[uint32]uint64)
	return sids
}
