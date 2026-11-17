//go:build windows

package runtime

import (
	"errors"
	"fmt"
	"io"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TOKEN_ELEVATION_TYPE values.
const (
	tokenElevationTypeFull    = 2
	tokenElevationTypeLimited = 3
)

// nativeSessionLogon is the production session logon. Only LocalSystem tests
// replace its functions, restoring them afterwards; the service never does.
var nativeSessionLogon = sessionLogonAPI{
	sessions:   loggedOnSessions,
	token:      querySessionToken,
	userSID:    tokenUserSID,
	filtered:   tokenFiltered,
	linked:     linkedFullToken,
	duplicate:  duplicateSessionToken,
	setSession: moveTokenToSessionZero,
}

// SessionSignedIn reports whether the account sid is still signed in to
// session id, by its token alone. A query that cannot tell counts as signed in.
func SessionSignedIn(id uint32, sid string) bool {
	return nativeSessionLogon.signedIn(id, sid)
}

// loggedOnSessions lists active and disconnected sessions. Session 0 and
// connected sessions, which have no user, are left out.
func loggedOnSessions() ([]loggedOnSession, error) {
	var info *windows.WTS_SESSION_INFO
	var count uint32
	if err := windows.WTSEnumerateSessions(0, 0, 1, &info, &count); err != nil {
		return nil, err
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(info)))
	if count > MaxInteractiveSessions {
		return nil, fmt.Errorf("session enumeration exceeds limit %d", MaxInteractiveSessions)
	}
	if count == 0 {
		return nil, nil
	}
	if info == nil {
		return nil, fmt.Errorf("session enumeration returned no data for %d sessions", count)
	}
	var out []loggedOnSession
	for _, s := range unsafe.Slice(info, int(count)) {
		if s.SessionID == 0 {
			continue
		}
		switch s.State {
		case windows.WTSActive:
			out = append(out, loggedOnSession{ID: s.SessionID, Active: true})
		case windows.WTSDisconnected:
			out = append(out, loggedOnSession{ID: s.SessionID})
		}
	}
	return out, nil
}

func querySessionToken(id uint32) (io.Closer, error) {
	var tok windows.Token
	err := windows.WTSQueryUserToken(id, &tok)
	switch {
	case err == nil:
		return winToken(tok), nil
	case errors.Is(err, windows.ERROR_NO_TOKEN), errors.Is(err, windows.ERROR_FILE_NOT_FOUND), errors.Is(err, windows.ERROR_CTX_WINSTATION_NOT_FOUND):
		return nil, errNoSessionUser
	default:
		return nil, err
	}
}

func handleOf(c io.Closer) windows.Token {
	t, _ := c.(winToken)
	return windows.Token(t)
}

func tokenUserSID(c io.Closer) (string, error) {
	user, err := handleOf(c).GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String(), nil
}

func tokenElevationType(tok windows.Token) (uint32, error) {
	var v, n uint32
	if err := windows.GetTokenInformation(tok, windows.TokenElevationType, (*byte)(unsafe.Pointer(&v)), uint32(unsafe.Sizeof(v)), &n); err != nil {
		return 0, err
	}
	return v, nil
}

func tokenFiltered(c io.Closer) (bool, error) {
	v, err := tokenElevationType(handleOf(c))
	return v == tokenElevationTypeLimited, err
}

// linkedFullToken returns the full token of a filtered administrator. With
// SeTcbPrivilege enabled Windows returns it as a primary token.
func linkedFullToken(c io.Closer) (io.Closer, error) {
	if err := withTcbPrivilege(); err != nil {
		return nil, fmt.Errorf("SeTcbPrivilege: %w", err)
	}
	linked, err := handleOf(c).GetLinkedToken()
	if err != nil {
		return nil, err
	}
	full := winToken(linked)
	v, err := tokenElevationType(linked)
	if err != nil {
		return full, err
	}
	if v != tokenElevationTypeFull {
		return full, fmt.Errorf("linked token has elevation type %d, not the full token", v)
	}
	return full, nil
}

func duplicateSessionToken(c io.Closer) (io.Closer, error) {
	primary, err := duplicatePrimary(handleOf(c))
	if primary != 0 {
		return winToken(primary), err
	}
	return nil, err
}

// moveTokenToSessionZero keeps the process in session 0, where the private
// desktop, the inherited handles and the unit's jobs are.
func moveTokenToSessionZero(c io.Closer) error {
	if err := withTcbPrivilege(); err != nil {
		return fmt.Errorf("SeTcbPrivilege: %w", err)
	}
	zero := uint32(0)
	return windows.SetTokenInformation(handleOf(c), windows.TokenSessionId, (*byte)(unsafe.Pointer(&zero)), uint32(unsafe.Sizeof(zero)))
}

// withTcbPrivilege enables SeTcbPrivilege on the manager's process token.
func withTcbPrivilege() error {
	owner := &UserToken{}
	err := enableSeTcbPrivilege(owner)
	return errors.Join(err, owner.Close())
}
