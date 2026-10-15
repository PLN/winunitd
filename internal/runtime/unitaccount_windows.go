//go:build windows

package runtime

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/sys/windows"
)

type accountPlatform struct {
	mu      sync.Mutex
	token   *UserToken
	keeper  UserManagerProc
	desktop string
	closed  bool
}

// OpenAccountSession logs the account on by trusted S4U, never with a stored
// credential, and starts its profile keeper on a private desktop. A failed
// open that could not release what it started returns the session with the
// error so the caller can retry Close.
func OpenAccountSession(spec AccountSessionSpec) (*AccountSession, error) {
	if !runningAsLocalSystem() {
		return nil, errors.New("User= requires the system manager running as LocalSystem")
	}
	info, err := LookupAccountName(spec.Account)
	if err != nil {
		return nil, err
	}
	tok, err := s4uLogon(LingerRecord{SID: info.SID, Name: FormatAccount(info)})
	if err != nil {
		return nil, fmt.Errorf("S4U logon for %s: %w", spec.Account, err)
	}
	s := &AccountSession{plat: accountPlatform{token: tok}}
	fail := func(cause error) (*AccountSession, error) {
		if err := s.Close(); err != nil {
			return s, errors.Join(cause, err)
		}
		return nil, cause
	}
	native, ok := nativeToken(tok)
	if !ok {
		return fail(errors.New("S4U logon returned no token"))
	}
	identity, err := native.GetTokenUser()
	if err != nil {
		return fail(err)
	}
	if got := identity.User.Sid.String(); !strings.EqualFold(got, info.SID) {
		return fail(fmt.Errorf("S4U token belongs to %s, not %s", got, info.SID))
	}
	keeper, desktop, err := startHeadlessProcess(native, UserManagerSpec{
		SID: info.SID, Token: tok, Exe: spec.Exe, Daemon: spec.Daemon, LoadProfile: true,
	}, []string{spec.Exe, ProfileKeeperFlag})
	s.plat.keeper = keeper
	if err != nil {
		return fail(fmt.Errorf("profile keeper for %s: %w", spec.Account, err))
	}
	s.plat.desktop = desktop
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
