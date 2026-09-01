package manager

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
)

// UserHostConfig launches per-SID user managers from the system daemon.
// Production QueryToken is WTSQueryUserToken (interactive). Linger uses
// trusted-LSA S4U first, then an optional named CredMan/LSA URI on the
// linger record only when S4U is insufficient for outbound network creds.
type UserHostConfig struct {
	Exe         string
	ExtraArgs   []string
	Daemon      *runtime.DaemonJob
	QueryToken  runtime.TokenSource
	Start       runtime.UserManagerLauncher
	Sessions    runtime.SessionEnumerator
	LingerDir   string
	Lookup      runtime.AccountLookup
	LingerToken runtime.LingerTokenFunc
	Logf        func(string, ...any)
}

// UserHost is SID-keyed (one manager per user), not session-keyed.
// First interactive logon starts the manager. Last logoff kills it
// unless the user is lingering.
type UserHost struct {
	cfg      UserHostConfig
	store    *LingerStore
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
	if cfg.Lookup == nil {
		cfg.Lookup = runtime.LookupAccountName
	}
	if cfg.LingerToken == nil {
		cfg.LingerToken = runtime.ObtainLingerToken
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.Exe == "" {
		if exe, err := os.Executable(); err == nil {
			cfg.Exe = exe
		}
	}
	h := &UserHost{
		cfg:      cfg,
		bySID:    make(map[string]*userInstance),
		sessions: make(map[uint32]string),
	}
	if cfg.LingerDir != "" {
		h.store = OpenLingerStore(cfg.LingerDir)
	}
	return h
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

// StartLingering starts user managers for linger records that have no
// session (S4U / named store). Call after Reconcile so logged-on users
// keep a WTS token.
func (h *UserHost) StartLingering() {
	if h == nil || h.store == nil {
		return
	}
	recs, err := h.store.List()
	if err != nil {
		h.cfg.Logf("list linger records: %v", err)
		return
	}
	for _, rec := range recs {
		if h.Alive(rec.SID) {
			continue
		}
		if err := h.startLinger(rec); err != nil {
			h.cfg.Logf("start lingering user manager %s: %v", rec.SID, err)
		}
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

	if err := h.ensureRunning(sid, tok); err != nil {
		h.cfg.Logf("start user manager %s: %v", sid, err)
		return
	}
	h.cfg.Logf("user manager %s started (session %d)", sid, sessionID)
}

func (h *UserHost) sidFromToken(tok *runtime.UserToken) string {
	if tok == nil {
		return ""
	}
	return tok.Info.SID
}

// Logoff records that a session ended. If that SID has no remaining
// sessions and is not lingering, the user manager is killed.
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
	if h.lingeringLocked(sid) {
		h.mu.Unlock()
		h.cfg.Logf("user manager %s lingering; keeping after last logoff", sid)
		return
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

func (h *UserHost) lingeringLocked(sid string) bool {
	return h.store != nil && h.store.Has(sid)
}

// Lingering reports whether sid has a linger record.
func (h *UserHost) Lingering(sid string) bool {
	if h == nil {
		return false
	}
	return h.store != nil && h.store.Has(sid)
}

// LingerCount is the number of linger records on disk.
func (h *UserHost) LingerCount() int {
	if h == nil || h.store == nil {
		return 0
	}
	recs, err := h.store.List()
	if err != nil {
		return 0
	}
	return len(recs)
}

// EnableLinger writes the linger record and starts a user manager if
// none is running (no session required).
func (h *UserHost) EnableLinger(user string) (*protocol.LingerResult, error) {
	if h == nil {
		return nil, protocol.ErrFailed("user host is not configured")
	}
	if h.store == nil {
		return nil, protocol.ErrFailed("linger store is not configured")
	}
	rec, err := h.resolve(user)
	if err != nil {
		return nil, protocol.ErrFailed(err.Error())
	}
	if err := h.store.Put(rec); err != nil {
		return nil, protocol.ErrFailed(err.Error())
	}
	if !h.Alive(rec.SID) {
		if err := h.startLinger(rec); err != nil {
			h.cfg.Logf("enable-linger start %s: %v", rec.SID, err)
		}
	}
	return &protocol.LingerResult{SID: rec.SID, User: rec.Name, Lingering: true}, nil
}

// DisableLinger removes the linger record. If no session remains, the
// manager is killed. If the user is still logged on, the manager stays
// until last logoff (P1 behavior).
func (h *UserHost) DisableLinger(user string) (*protocol.LingerResult, error) {
	if h == nil {
		return nil, protocol.ErrFailed("user host is not configured")
	}
	if h.store == nil {
		return nil, protocol.ErrFailed("linger store is not configured")
	}
	rec, err := h.resolve(user)
	if err != nil {
		return nil, protocol.ErrFailed(err.Error())
	}
	if err := h.store.Delete(rec.SID); err != nil {
		return nil, protocol.ErrFailed(err.Error())
	}

	h.mu.Lock()
	hasSession := false
	for _, mapped := range h.sessions {
		if mapped == rec.SID {
			hasSession = true
			break
		}
	}
	inst := h.bySID[rec.SID]
	if !hasSession {
		delete(h.bySID, rec.SID)
	}
	h.mu.Unlock()

	if !hasSession && inst != nil && inst.proc != nil {
		if err := inst.proc.Kill(); err != nil {
			h.cfg.Logf("disable-linger kill %s: %v", rec.SID, err)
		}
	}
	return &protocol.LingerResult{SID: rec.SID, User: rec.Name, Lingering: false}, nil
}

func (h *UserHost) resolve(user string) (runtime.LingerRecord, error) {
	user = strings.TrimSpace(user)
	if user == "" {
		return runtime.LingerRecord{}, fmt.Errorf("user name required")
	}
	if protocol.ValidSID(user) {
		rec := runtime.LingerRecord{SID: user}
		if h.cfg.Lookup != nil {
			if info, err := h.cfg.Lookup(user); err == nil {
				if info.SID != "" {
					rec.SID = info.SID
				}
				rec.Name = runtime.FormatAccount(info)
			}
		}
		if existing, err := h.store.Get(rec.SID); err == nil {
			rec.CredentialURI = existing.CredentialURI
			if rec.Name == "" {
				rec.Name = existing.Name
			}
		}
		return rec, nil
	}
	if h.cfg.Lookup == nil {
		return runtime.LingerRecord{}, fmt.Errorf("cannot resolve user %q", user)
	}
	info, err := h.cfg.Lookup(user)
	if err != nil {
		return runtime.LingerRecord{}, err
	}
	if !protocol.ValidSID(info.SID) {
		return runtime.LingerRecord{}, fmt.Errorf("lookup of %q returned invalid SID %q", user, info.SID)
	}
	rec := runtime.LingerRecord{SID: info.SID, Name: runtime.FormatAccount(info)}
	if rec.Name == "" {
		rec.Name = user
	}
	if existing, err := h.store.Get(rec.SID); err == nil {
		rec.CredentialURI = existing.CredentialURI
	}
	return rec, nil
}

func (h *UserHost) startLinger(rec runtime.LingerRecord) error {
	if h.cfg.LingerToken == nil {
		return runtime.ErrNoLingerToken
	}
	tok, err := h.cfg.LingerToken(rec)
	if err != nil {
		return err
	}
	if tok != nil {
		defer tok.Close()
		if tok.Source != "" {
			h.cfg.Logf("linger token for %s via %s", rec.SID, tok.Source)
		}
	}
	return h.ensureRunning(rec.SID, tok)
}

func (h *UserHost) ensureRunning(sid string, tok *runtime.UserToken) error {
	h.mu.Lock()
	if inst := h.bySID[sid]; inst != nil && inst.proc != nil && inst.proc.Alive() {
		h.mu.Unlock()
		return nil
	}
	h.mu.Unlock()

	spec := runtime.UserManagerSpec{
		SID:       sid,
		Token:     tok,
		Exe:       h.cfg.Exe,
		ExtraArgs: append([]string(nil), h.cfg.ExtraArgs...),
		Daemon:    h.cfg.Daemon,
	}
	if tok != nil {
		spec.Env = runtime.MergeDeterministicUserEnv(os.Environ(), tok.Info)
	}
	proc, err := h.cfg.Start(spec)
	if err != nil {
		return err
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if inst := h.bySID[sid]; inst != nil && inst.proc != nil && inst.proc.Alive() {
		_ = proc.Kill()
		return nil
	}
	h.bySID[sid] = &userInstance{sid: sid, proc: proc}
	return nil
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

// Close kills every user manager (system shutdown).
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
	return fmt.Sprintf("userhost managers=%d lingering=%d", h.ManagerCount(), h.LingerCount())
}
