//go:build windows

package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/sys/windows"
)

type accountPlatform struct {
	mu          sync.Mutex
	token       *UserToken
	keeper      UserManagerProc
	desktop     string
	closing     bool
	closed      bool
	keeperEnded chan struct{}
	onExit      func()
}

// OpenAccountSession logs the account on, never with a stored credential, and
// starts its profile keeper on a private desktop. The logon is trusted S4U,
// or with spec.Session first a copy of a signed-in session's token. With
// spec.From it is a copy of that session's token and nothing logs on. A failed
// open that could not release what it started returns the session with the
// error so the caller can retry Close.
func OpenAccountSession(spec AccountSessionSpec) (*AccountSession, error) {
	if !runningAsLocalSystem() {
		return nil, errors.New("User= requires the system manager running as LocalSystem")
	}
	if spec.From != nil {
		return openFromAccount(spec)
	}
	info, err := LookupAccountName(spec.Account)
	if err != nil {
		return nil, err
	}
	return openAccountLogons(spec.Session,
		func() (*AccountSession, error) { return openSessionAccount(nativeSessionLogon, spec, info) },
		func() (*AccountSession, error) { return openS4UAccount(spec, info) })
}

func openS4UAccount(spec AccountSessionSpec, info UserInfo) (*AccountSession, error) {
	tok, err := s4uLogon(LingerRecord{SID: info.SID, Name: FormatAccount(info)})
	if err != nil {
		// A failed logon can still return a token whose cleanup must be retried.
		s := &AccountSession{plat: accountPlatform{token: tok}}
		cause := fmt.Errorf("S4U logon for %s: %w", spec.Account, err)
		if err := s.Close(); err != nil {
			return s, errors.Join(cause, err)
		}
		return nil, cause
	}
	return finishOpen(spec, info, tok, LogonS4U, 0)
}

// openSessionAccount opens the account session with the token of a signed-in
// session of the account. Every failure names that session; one whose session
// signed out meanwhile is a sessionGoneError, never followed by S4U.
func openSessionAccount(api sessionLogonAPI, spec AccountSessionSpec, info UserInfo) (*AccountSession, error) {
	tok, id, selected, err := api.acquire(info.SID)
	if err == nil {
		var s *AccountSession
		s, err = finishOpen(spec, info, tok, LogonSession, id)
		if err == nil {
			return s, nil
		}
		err = fmt.Errorf("session %d: %w", id, err)
		if s != nil {
			return s, err
		}
	} else if tok != nil {
		return &AccountSession{plat: accountPlatform{token: tok}}, err
	}
	if selected && !api.signedIn(id, info.SID) {
		return nil, &sessionGoneError{ID: id}
	}
	return nil, err
}

// openFromAccount opens an account session of its own on a duplicate of the
// token spec.From holds: the invocation's logon, which stays valid after its
// session signed out and needs neither that session nor a domain controller.
// The account is the one spec.From was opened for, so nothing is looked up.
func openFromAccount(spec AccountSessionSpec) (*AccountSession, error) {
	from := spec.From
	tok, err := from.duplicateToken()
	if err != nil {
		return nil, fmt.Errorf("invocation's logon: %w", err)
	}
	return finishOpen(spec, from.Info, tok, from.Logon, from.SessionID)
}

// duplicateToken returns a new primary token duplicated from the session's,
// which it neither closes nor changes. It fails once the session is closed.
func (s *AccountSession) duplicateToken() (*UserToken, error) {
	s.plat.mu.Lock()
	defer s.plat.mu.Unlock()
	if s.plat.closed {
		return nil, errors.New("account session is closed")
	}
	native, ok := nativeToken(s.plat.token)
	if !ok {
		return nil, errors.New("account session has no token")
	}
	dup, err := duplicatePrimary(native)
	if err != nil {
		return nil, err
	}
	return &UserToken{Info: s.Info, native: winToken(dup)}, nil
}

// finishOpen starts the profile keeper for tok, which it owns from here on,
// and builds the account's environment.
func finishOpen(spec AccountSessionSpec, info UserInfo, tok *UserToken, logon string, sessionID uint32) (*AccountSession, error) {
	s := &AccountSession{Logon: logon, SessionID: sessionID, plat: accountPlatform{token: tok}}
	fail := func(cause error) (*AccountSession, error) {
		if err := s.Close(); err != nil {
			return s, errors.Join(cause, err)
		}
		return nil, cause
	}
	kind := "S4U"
	if logon == LogonSession {
		kind = "session"
	}
	native, ok := nativeToken(tok)
	if !ok {
		return fail(fmt.Errorf("%s logon returned no token", kind))
	}
	identity, err := native.GetTokenUser()
	if err != nil {
		return fail(err)
	}
	if got := identity.User.Sid.String(); !strings.EqualFold(got, info.SID) {
		return fail(fmt.Errorf("%s token belongs to %s, not %s", kind, got, info.SID))
	}
	keeper, desktop, err := startHeadlessProcess(native, UserManagerSpec{
		SID: info.SID, Token: tok, Exe: spec.Exe, Daemon: spec.Daemon, LoadProfile: true,
	}, []string{spec.Exe, ProfileKeeperFlag})
	s.plat.keeper = keeper
	if err != nil {
		return fail(fmt.Errorf("profile keeper for %s: %w", spec.Account, err))
	}
	s.plat.desktop = desktop
	s.plat.keeperEnded = make(chan struct{})
	go s.watchKeeper(keeper)
	// The keeper's launch loaded the profile, so the token's environment and
	// known folders now resolve.
	env, err := native.Environ(false)
	if err != nil {
		return fail(err)
	}
	if !hasUserProfile(env) {
		return fail(fmt.Errorf("profile of %s is unavailable", spec.Account))
	}
	full, err := userInfoFromToken(native)
	if err != nil {
		return fail(err)
	}
	s.Info = full
	s.env = MergeDeterministicUserEnv(env, full)
	return s, nil
}

func hasUserProfile(env []string) bool {
	for _, entry := range env {
		name, value, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(name, "USERPROFILE") && value != "" {
			return true
		}
	}
	return false
}

func (s *AccountSession) watchKeeper(keeper UserManagerProc) {
	_ = keeper.Wait(context.Background())
	s.plat.mu.Lock()
	close(s.plat.keeperEnded)
	onExit := s.plat.onExit
	if s.plat.closing {
		onExit = nil
	}
	s.plat.mu.Unlock()
	if onExit != nil {
		onExit()
	}
}

// onKeeperExit runs fn if the profile keeper ends before Close, at once if it
// already has.
func (s *AccountSession) onKeeperExit(fn func()) {
	s.plat.mu.Lock()
	s.plat.onExit = fn
	ended := false
	select {
	case <-s.plat.keeperEnded:
		ended = !s.plat.closing
	default:
	}
	s.plat.mu.Unlock()
	if ended {
		fn()
	}
}

// launchIdentity is the token and desktop for CreateProcessAsUser.
func (s *AccountSession) launchIdentity() (windows.Token, *uint16, error) {
	s.plat.mu.Lock()
	defer s.plat.mu.Unlock()
	if s.plat.closed || s.plat.keeper == nil || !s.plat.keeper.Alive() {
		return 0, nil, errors.New("account session is closed or its profile keeper has ended")
	}
	tok, ok := nativeToken(s.plat.token)
	if !ok {
		return 0, nil, errors.New("account session has no token")
	}
	desktop, err := windows.UTF16PtrFromString(s.plat.desktop)
	if err != nil {
		return 0, nil, err
	}
	return tok, desktop, nil
}

// Close ends the profile keeper, which releases the desktop and lets Windows
// unload the profile, and closes the token. Call it after the unit's
// processes have ended. A failure keeps what remains for a retry.
func (s *AccountSession) Close() error {
	if s == nil {
		return nil
	}
	s.plat.mu.Lock()
	defer s.plat.mu.Unlock()
	if s.plat.closed {
		return nil
	}
	s.plat.closing = true
	if s.plat.keeper != nil {
		if err := s.plat.keeper.Kill(); err != nil {
			return fmt.Errorf("stop profile keeper: %w", err)
		}
		s.plat.keeper = nil
	}
	if s.plat.token != nil {
		if err := s.plat.token.Close(); err != nil {
			return fmt.Errorf("close account token: %w", err)
		}
		s.plat.token = nil
	}
	s.plat.closed = true
	return nil
}
