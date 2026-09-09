package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/timers"
)

// UserHostConfig launches per-SID user managers from the system daemon.
// Production QueryToken is WTSQueryUserToken (interactive). Linger uses
// trusted-LSA S4U first, then an optional named CredMan/LSA URI on the
// linger record only when S4U is insufficient for outbound network creds.
type UserHostConfig struct {
	Admission      UserAdmission
	ProbeUserUnits func(*runtime.UserToken) (bool, error)
	Exe            string
	ExtraArgs      []string
	Daemon         *runtime.DaemonJob
	QueryToken     runtime.TokenSource
	Start          runtime.UserManagerLauncher
	Sessions       runtime.SessionEnumerator
	LingerDir      string
	Lookup         runtime.AccountLookup
	LingerToken    runtime.LingerTokenFunc
	Logf           func(string, ...any)
}

// UserHost is SID-keyed (one manager per user), not session-keyed.
// First interactive logon starts the manager. Last logoff kills it
// unless the user is lingering.
type UserHost struct {
	admission          UserAdmission
	admissionRevision  uint64
	closed             bool
	ops                unitOps
	stops              stopSet
	cfg                UserHostConfig
	store              *LingerStore
	mu                 sync.Mutex
	bySID              map[string]*userInstance
	sessions           map[uint32]string // session ID -> SID
	sessionRequests    map[uint32]uint64
	nextSessionRequest uint64
}

type userInstance struct {
	uncertain bool
	err       string
	sid       string
	proc      runtime.UserManagerProc
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
	policy, err := cfg.Admission.validated()
	if err != nil {
		cfg.Logf("user admission: %v", err)
		policy, _ = (UserAdmission{}).validated()
	}
	if cfg.ProbeUserUnits == nil {
		cfg.ProbeUserUnits = runtime.UserUnitFilesPresent
	}
	if cfg.Exe == "" {
		if exe, err := os.Executable(); err == nil {
			cfg.Exe = exe
		}
	}
	h := &UserHost{
		admission:       policy,
		cfg:             cfg,
		bySID:           make(map[string]*userInstance),
		sessions:        make(map[uint32]string),
		sessionRequests: make(map[uint32]uint64),
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
	// Register the request before token lookup, which can outlive logoff or
	// another logon that reuses the same session ID.
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.nextSessionRequest++
	request := h.nextSessionRequest
	h.sessionRequests[sessionID] = request
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		if h.sessionRequests[sessionID] == request {
			delete(h.sessionRequests, sessionID)
		}
		h.mu.Unlock()
	}()
	h.mu.Lock()
	revision := h.admissionRevision
	policy := h.admission
	h.mu.Unlock()
	current := func() bool { return h.sessionRequests[sessionID] == request && h.admissionRevision == revision }
	tok, err := h.cfg.QueryToken(sessionID)
	if err != nil {
		h.cfg.Logf("session %d: %v", sessionID, err)
		return
	}
	if tok == nil {
		h.cfg.Logf("session %d: missing user token", sessionID)
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
	allow, probe := policy.decision(sid)
	if probe {
		allow, err = h.cfg.ProbeUserUnits(tok)
		if err != nil {
			h.cfg.Logf("user admission probe: %v", err)
			return
		}
	}
	if !allow {
		return
	}

	h.mu.Lock()
	if h.closed || !current() {
		h.mu.Unlock()
		return
	}
	h.sessions[sessionID] = sid
	h.mu.Unlock()

	if err := h.ensureRunning(sid, tok, current); err != nil {
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
	sid := h.sessions[sessionID]
	delete(h.sessionRequests, sessionID)
	delete(h.sessions, sessionID)
	h.mu.Unlock()
	if sid == "" {
		return
	}
	if err := h.stopUser(context.Background(), sid, true); err != nil {
		h.cfg.Logf("kill user manager %s: %v", sid, err)
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
	h.mu.Lock()
	closed := h.closed
	h.mu.Unlock()
	if closed {
		return nil, protocol.ErrFailed("user host is shutting down or closed")
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

	result := &protocol.LingerResult{SID: rec.SID, User: rec.Name, Lingering: false}
	if err := h.stopUser(context.Background(), rec.SID, true); err != nil {
		return result, protocol.ErrFailed(err.Error())
	}
	return result, nil
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
	return h.ensureRunning(rec.SID, tok, func() bool { return h.lingeringLocked(rec.SID) })
}

// stillWanted is evaluated only while h.mu is held.
func (h *UserHost) ensureRunning(sid string, tok *runtime.UserToken, stillWanted func() bool) error {
	unlock := h.ops.lock(sid)
	defer unlock()
	inst, err := h.inspectUserLaunch(sid, stillWanted)
	if err != nil {
		return err
	}
	// The SID gate retains this instance while liveness is observed outside h.mu.
	if inst != nil && inst.proc != nil && inst.proc.Alive() {
		return nil
	}
	if inst != nil && inst.proc != nil {
		if err := h.killUserInstance(context.Background(), sid, inst); err != nil {
			return err
		}
	}
	inst, err = h.acceptUserLaunch(sid, stillWanted)
	if err != nil {
		return err
	}
	spec := runtime.UserManagerSpec{
		SID: sid, Token: tok, Exe: h.cfg.Exe,
		ExtraArgs: append([]string(nil), h.cfg.ExtraArgs...), Daemon: h.cfg.Daemon,
	}
	if tok != nil {
		spec.Env = runtime.MergeDeterministicUserEnv(os.Environ(), tok.Info)
	}
	proc, err := h.cfg.Start(spec)
	superseded := h.applyUserLaunch(sid, inst, proc, err, stillWanted)
	if err != nil {
		return err
	}
	if proc == nil {
		return fmt.Errorf("user manager launcher returned no process")
	}
	if superseded {
		return errors.Join(fmt.Errorf("user manager launch superseded"), h.killUserInstance(context.Background(), sid, inst))
	}
	return nil
}

// stopUser serializes with accepted launches and rechecks the idle policy after
// acquiring the operation lock. A concurrent logon cannot lose its replacement.
func (h *UserHost) stopUser(ctx context.Context, sid string, onlyIdle bool) error {
	unlock, err := h.ops.lockContext(ctx, sid)
	if err != nil {
		return err
	}
	defer unlock()
	h.mu.Lock()
	if onlyIdle && !h.closed {
		allow, probe := h.admission.decision(sid)
		for _, mapped := range h.sessions {
			if mapped == sid && (allow || probe) {
				h.mu.Unlock()
				return nil
			}
		}
		if h.lingeringLocked(sid) {
			h.mu.Unlock()
			return nil
		}
	}
	inst := h.bySID[sid]
	h.mu.Unlock()
	return h.killUserInstance(ctx, sid, inst)
}

// Caller owns the per-SID operation lock; failed or pending kills keep the record.
func (h *UserHost) killUserInstance(ctx context.Context, sid string, inst *userInstance) error {
	if inst == nil || inst.proc == nil {
		return nil
	}
	proc := h.acceptUserCleanup(inst)
	err := h.stops.wait(ctx, timers.DefaultClock(), stopKey{user: proc}, defaultStopTimeout, proc.Kill)
	if err == nil && proc.Alive() {
		err = fmt.Errorf("user manager remains alive after termination")
	}
	h.applyUserCleanup(sid, inst, err)
	return err
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

// Close requests bounded shutdown and logs unresolved cleanup. Shutdown exposes
// the error to callers that must prove termination before proceeding.
func (h *UserHost) Close() {
	if h == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultStopTimeout)
	defer cancel()
	if err := h.Shutdown(ctx); err != nil {
		h.cfg.Logf("user host shutdown: %v", err)
	}
}

// Shutdown closes admission, drains accepted launches, and retains failed cleanup
// for a later retry. The supplied context bounds waits across all user managers.
func (h *UserHost) Shutdown(ctx context.Context) error {
	if h == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	sids := h.acceptUserShutdown()
	var result error
	for _, sid := range sids {
		if err := h.stopUser(ctx, sid, false); err != nil {
			result = errors.Join(result, err)
		}
	}
	return errors.Join(result, ctx.Err())
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
