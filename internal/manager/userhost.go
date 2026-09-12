package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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
	CloseToken     func(*runtime.UserToken) error
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
	nativeWork         map[*userNativeWork]struct{}
	idleDispatch       *userIdleDispatch
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
	if cfg.CloseToken == nil {
		cfg.CloseToken = (*runtime.UserToken).Close
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
				h.dispatchLogon(sc.SessionID, true)
			} else {
				h.queueIdleCleanup(h.recordLogoff(sc.SessionID))
			}
		}
	}
}

// Reconcile repairs missed session notifications and recovers exited managers.
// An enumeration that overlaps a newer session/policy decision is discarded.
func (h *UserHost) Reconcile() {
	if h == nil || h.cfg.Sessions == nil {
		return
	}
	work, err := h.acceptUserWorkClass(userWorkReconcile)
	if err != nil {
		return
	}
	h.reconcileAccepted(work)
}

func (h *UserHost) scheduleReconcile() {
	work, err := h.acceptUserWorkClass(userWorkReconcile)
	if err != nil {
		return
	}
	go h.reconcileAccepted(work)
}

func (h *UserHost) reconcileAccepted(work *userNativeWork) {
	defer h.finishNativeUserWork(work, nil)
	cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), time.Second)
	if err := h.cleanupNativeUserWork(cleanupCtx, false); err != nil {
		h.cfg.Logf("retry user token cleanup: %v", err)
	}
	cancelCleanup()
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	epoch, revision := h.nextSessionRequest, h.admissionRevision
	h.mu.Unlock()
	ids, err := h.cfg.Sessions()
	if err != nil {
		h.cfg.Logf("enumerate sessions: %v", err)
		return
	}
	requests, sids := h.acceptUserSessions(ids, epoch, revision)
	for id, request := range requests {
		h.queryUserLogon(id, request)
	}
	// Query current sessions first: a replacement session for the same SID
	// must be recorded before deciding whether its existing manager is idle.
	for _, sid := range sids {
		if err := h.stopUser(context.Background(), sid, true); err != nil {
			h.cfg.Logf("reconcile user manager %s: %v", sid, err)
		}
	}
}

// StartLingering starts user managers for linger records that have no
// session (S4U / named store). Call after Reconcile so logged-on users
// keep a WTS token.
func (h *UserHost) StartLingering() {
	if h == nil || h.store == nil {
		return
	}
	work, err := h.acceptUserWorkClass(userWorkLingerScan)
	if err != nil {
		return
	}
	defer h.finishNativeUserWork(work, nil)
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
	h.dispatchLogon(sessionID, false)
}

// Reserve native work before spawning. Notification storms cannot accumulate
// unadmitted goroutines, and a blocked token lookup cannot hide later logoff.
func (h *UserHost) dispatchLogon(sessionID uint32, asynchronous bool) {
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
	work, err := h.acceptNativeUserWorkLocked()
	if err == nil {
		h.sessionRequests[sessionID] = request
	} else {
		delete(h.sessionRequests, sessionID)
	}
	h.mu.Unlock()
	if err != nil {
		h.cfg.Logf("session %d admission: %v", sessionID, err)
		return
	}
	if asynchronous {
		go h.queryAcceptedUserLogon(sessionID, request, work)
	} else {
		h.queryAcceptedUserLogon(sessionID, request, work)
	}
}

func (h *UserHost) finishSessionRequest(sessionID uint32, request uint64) {
	h.mu.Lock()
	if h.sessionRequests[sessionID] == request {
		delete(h.sessionRequests, sessionID)
	}
	h.mu.Unlock()
}

func (h *UserHost) queryUserLogon(sessionID uint32, request uint64) {
	work, err := h.acceptNativeUserWork()
	if err != nil {
		h.finishSessionRequest(sessionID, request)
		h.cfg.Logf("session %d admission: %v", sessionID, err)
		return
	}
	h.queryAcceptedUserLogon(sessionID, request, work)
}

func (h *UserHost) queryAcceptedUserLogon(sessionID uint32, request uint64, work *userNativeWork) {
	defer h.finishSessionRequest(sessionID, request)
	var tok *runtime.UserToken
	defer func() {
		if err := h.finishNativeUserWork(work, tok); err != nil {
			h.cfg.Logf("session %d token cleanup: %v", sessionID, err)
		}
	}()
	h.mu.Lock()
	if h.closed || h.sessionRequests[sessionID] != request {
		h.mu.Unlock()
		return
	}
	revision := h.admissionRevision
	policy := h.admission
	h.mu.Unlock()
	current := func() bool { return h.sessionRequests[sessionID] == request && h.admissionRevision == revision }
	var err error
	tok, err = h.cfg.QueryToken(sessionID)
	if err != nil {
		h.cfg.Logf("session %d: %v", sessionID, err)
		return
	}
	if tok == nil {
		h.cfg.Logf("session %d: missing user token", sessionID)
		return
	}
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
	sid := h.recordLogoff(sessionID)
	if sid == "" {
		return
	}
	if err := h.stopUser(context.Background(), sid, true); err != nil {
		h.cfg.Logf("kill user manager %s: %v", sid, err)
	}
}

func (h *UserHost) recordLogoff(sessionID uint32) string {
	h.mu.Lock()
	h.nextSessionRequest++ // invalidate any enumeration started before logoff
	sid := h.sessions[sessionID]
	delete(h.sessionRequests, sessionID)
	delete(h.sessions, sessionID)
	h.mu.Unlock()
	return sid
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
	rec, err := h.mutateLingerRecord(user, true)
	if err != nil {
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
	rec, err := h.mutateLingerRecord(user, false)
	if err != nil {
		return nil, protocol.ErrFailed(err.Error())
	}

	result := &protocol.LingerResult{SID: rec.SID, User: rec.Name, Lingering: false}
	if err := h.stopUser(context.Background(), rec.SID, true); err != nil {
		return result, protocol.ErrFailed(err.Error())
	}
	return result, nil
}

// Accepted account lookup and record persistence stay visible to shutdown, even
// before a SID is known. Subsequent launch admission separately rechecks closure.
func (h *UserHost) mutateLingerRecord(user string, enable bool) (runtime.LingerRecord, error) {
	work, err := h.acceptNativeUserWork()
	if err != nil {
		return runtime.LingerRecord{}, err
	}
	defer h.finishNativeUserWork(work, nil)
	rec, err := h.resolve(user)
	if err != nil {
		return rec, err
	}
	if enable {
		err = h.store.Put(rec)
	} else {
		err = h.store.Delete(rec.SID)
	}
	return rec, err
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

func (h *UserHost) startLinger(rec runtime.LingerRecord) (startErr error) {
	if h.cfg.LingerToken == nil {
		return runtime.ErrNoLingerToken
	}
	work, err := h.acceptNativeUserWork()
	if err != nil {
		return err
	}
	var tok *runtime.UserToken
	defer func() { startErr = errors.Join(startErr, h.finishNativeUserWork(work, tok)) }()
	tok, err = h.cfg.LingerToken(rec)
	if err != nil {
		return err
	}
	if tok != nil {
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
		LoadProfile: true,
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
	return h.stopUserLocked(ctx, sid, onlyIdle)
}

// Caller owns the SID gate.
func (h *UserHost) stopUserLocked(ctx context.Context, sid string, onlyIdle bool) error {
	h.mu.Lock()
	inst := h.bySID[sid]
	// A later logon cannot cancel cleanup that already started. Finish the
	// retained obligation before reconciliation may create its replacement.
	if onlyIdle && !h.closed && (inst == nil || !inst.uncertain) {
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
	h.mu.Unlock()
	return h.killUserInstance(ctx, sid, inst)
}

// Caller owns the per-SID operation lock; failed or pending kills keep the record.
func (h *UserHost) killUserInstance(ctx context.Context, sid string, inst *userInstance) error {
	if inst == nil || inst.proc == nil {
		return nil
	}
	proc := h.acceptUserCleanup(inst)
	err := h.stops.wait(ctx, timers.DefaultClock(), stopKey{user: proc}, defaultStopTimeout, func() error {
		if err := proc.Kill(); err != nil {
			return err
		}
		if proc.Alive() {
			return fmt.Errorf("user manager remains alive after termination")
		}
		return nil
	})
	h.applyUserCleanup(sid, inst, err)
	return err
}

// Alive reports whether a manager is running for sid.
func (h *UserHost) Alive(sid string) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	var proc runtime.UserManagerProc
	if inst := h.bySID[sid]; inst != nil {
		proc = inst.proc
	}
	h.mu.Unlock()
	return proc != nil && proc.Alive()
}

// Running returns SIDs with a live manager.
func (h *UserHost) Running() []string {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	procs := make(map[string]runtime.UserManagerProc, len(h.bySID))
	for sid, inst := range h.bySID {
		if inst != nil && inst.proc != nil {
			procs[sid] = inst.proc
		}
	}
	h.mu.Unlock()
	out := make([]string, 0, len(procs))
	for sid, proc := range procs {
		if proc.Alive() {
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
	h.mu.Lock()
	h.sealShutdownLocked()
	h.mu.Unlock()
	for {
		var ownsPass atomic.Bool
		err := h.stops.wait(ctx, timers.DefaultClock(), stopKey{userShutdown: h}, 0, func() error {
			ownsPass.Store(true)
			return h.shutdownPass(ctx)
		})
		if ctx.Err() == nil && !ownsPass.Load() && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) {
			continue
		}
		return errors.Join(err, ctx.Err())
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
