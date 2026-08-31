package manager

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
)

// UserHostConfig launches per-SID user managers from the system daemon.
// Production QueryToken is WTSQueryUserToken and Start is CreateProcessAsUser.
// Tests inject fakes. There is no password or credential-store fallback.
type UserHostConfig struct {
	Exe        string
	ExtraArgs  []string
	Daemon     *runtime.DaemonJob
	QueryToken runtime.TokenSource
	Start      runtime.UserManagerLauncher
	Sessions   runtime.SessionEnumerator
	Logf       func(string, ...any)
}

// UserHost is SID-keyed (one manager per user), not session-keyed.
// First interactive logon starts the manager; last logoff with no linger
// kills it (P1 has no linger).
type UserHost struct {
	cfg      UserHostConfig
	mu       sync.Mutex
	bySID    map[string]*userInstance
	sessions map[uint32]string // session ID -> SID
}

type userInstance struct {
	sid  string
	proc runtime.UserManagerProc
}

// NewUserHost creates a host. Call Listen after the system manager is up.
func NewUserHost(cfg UserHostConfig) *UserHost {
	if cfg.QueryToken == nil {
		cfg.QueryToken = runtime.QueryUserToken
	}
	if cfg.Start == nil {
		cfg.Start = runtime.StartUserManager
	}
	if cfg.Sessions == nil {
		cfg.Sessions = runtime.InteractiveSessions
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.Exe == "" {
		if exe, err := os.Executable(); err == nil {
			cfg.Exe = exe
		}
	}
	return &UserHost{
		cfg:      cfg,
		bySID:    make(map[string]*userInstance),
		sessions: make(map[uint32]string),
	}
}

// Listen consumes session changes until ctx is done.
func (h *UserHost) Listen(ctx context.Context, ch <-chan runtime.SessionChange) {
	if h == nil || ch == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case sc, ok := <-ch:
			if !ok {
				return
			}
			if sc.Logon {
				h.Logon(sc.SessionID)
			} else {
				h.Logoff(sc.SessionID)
			}
		}
	}
}

// Reconcile starts managers for sessions that are already logged on.
func (h *UserHost) Reconcile() {
	if h == nil || h.cfg.Sessions == nil {
		return
	}
	ids, err := h.cfg.Sessions()
	if err != nil {
		h.cfg.Logf("enumerate sessions: %v", err)
		return
	}
	for _, id := range ids {
		h.Logon(id)
	}
}

// Logon starts a user manager for the session's SID if none is running.
func (h *UserHost) Logon(sessionID uint32) {
	if h == nil {
		return
	}
	tok, err := h.cfg.QueryToken(sessionID)
	if err != nil {
		h.cfg.Logf("session %d: %v", sessionID, err)
		return
	}
	defer tok.Close()
	sid := tok.Info.SID
	if sid == "" {
		sid = h.sidFromToken(tok)
	}
	if !protocol.ValidSID(sid) {
		h.cfg.Logf("session %d: invalid SID %q", sessionID, sid)
		return
	}

	h.mu.Lock()
	h.sessions[sessionID] = sid
	if inst := h.bySID[sid]; inst != nil && inst.proc != nil && inst.proc.Alive() {
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()

	spec := runtime.UserManagerSpec{
		SID:       sid,
		Token:     tok,
		Exe:       h.cfg.Exe,
		ExtraArgs: append([]string(nil), h.cfg.ExtraArgs...),
		Env:       runtime.MergeDeterministicUserEnv(os.Environ(), tok.Info),
		Daemon:    h.cfg.Daemon,
	}
	proc, err := h.cfg.Start(spec)
	if err != nil {
		h.cfg.Logf("start user manager %s: %v", sid, err)
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if inst := h.bySID[sid]; inst != nil && inst.proc != nil && inst.proc.Alive() {
		_ = proc.Kill()
		return
	}
	h.bySID[sid] = &userInstance{sid: sid, proc: proc}
	h.sessions[sessionID] = sid
	h.cfg.Logf("user manager %s started (session %d)", sid, sessionID)
}

func (h *UserHost) sidFromToken(tok *runtime.UserToken) string {
	if tok == nil {
		return ""
	}
	return tok.Info.SID
}

// Logoff records that a session ended. If that SID has no remaining
// sessions, the user manager is killed (no linger).
func (h *UserHost) Logoff(sessionID uint32) {
	if h == nil {
		return
	}
	h.mu.Lock()
	sid, ok := h.sessions[sessionID]
	if ok {
		delete(h.sessions, sessionID)
	}
	if sid == "" {
		h.mu.Unlock()
		return
	}
	for _, mapped := range h.sessions {
		if mapped == sid {
			h.mu.Unlock()
			return
		}
	}
	inst := h.bySID[sid]
	delete(h.bySID, sid)
	h.mu.Unlock()

	if inst != nil && inst.proc != nil {
		if err := inst.proc.Kill(); err != nil {
			h.cfg.Logf("kill user manager %s: %v", sid, err)
		} else {
			h.cfg.Logf("user manager %s stopped (session %d logoff)", sid, sessionID)
		}
	}
}

// Alive reports whether a manager is running for sid.
func (h *UserHost) Alive(sid string) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	inst := h.bySID[sid]
	return inst != nil && inst.proc != nil && inst.proc.Alive()
}

// Running returns SIDs with a live manager.
func (h *UserHost) Running() []string {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.bySID))
	for sid, inst := range h.bySID {
		if inst != nil && inst.proc != nil && inst.proc.Alive() {
			out = append(out, sid)
		}
	}
	return out
}

// Close kills every user manager (system shutdown / no linger).
func (h *UserHost) Close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	insts := make([]*userInstance, 0, len(h.bySID))
	for sid, inst := range h.bySID {
		insts = append(insts, inst)
		delete(h.bySID, sid)
	}
	h.sessions = make(map[uint32]string)
	h.mu.Unlock()
	for _, inst := range insts {
		if inst != nil && inst.proc != nil {
			_ = inst.proc.Kill()
		}
	}
}

// ManagerCount is the number of tracked user managers (live or not yet reaped).
func (h *UserHost) ManagerCount() int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.bySID)
}

func (h *UserHost) String() string {
	if h == nil {
		return "userhost<nil>"
	}
	return fmt.Sprintf("userhost managers=%d", h.ManagerCount())
}
