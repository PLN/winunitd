package runtime

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Logon kinds of an account session (UserLogon=).
const (
	LogonS4U     = "s4u"
	LogonSession = "session"
)

// AccountLogon is implemented by a process that runs as a User= account. It
// reports the logon its account session used, LogonS4U or LogonSession, and
// for LogonSession the session whose token it took. Processes without an
// account session, and test fakes, need not implement it.
type AccountLogon interface {
	AccountLogon() (logon string, session uint32)
}

// errNoSessionUser is a token query's answer that nobody is signed in to the
// session, or that the session no longer exists.
var errNoSessionUser = errors.New("no user is signed in to the session")

// errNoSignedInSession is the session half of a failed open when no session
// of the account was found.
var errNoSignedInSession = errors.New("no signed-in session of the account")

// sessionGoneError reports that the selected session signed out while the
// launch was under way. It is a failed start, never followed by S4U.
type sessionGoneError struct {
	ID    uint32
	Cause error // the launch error it replaces, if any
}

func (e *sessionGoneError) Error() string {
	msg := fmt.Sprintf("session %d: signed out during the launch", e.ID)
	if e.Cause != nil {
		msg += ": " + e.Cause.Error()
	}
	return msg
}

func (e *sessionGoneError) Unwrap() error { return e.Cause }

// loggedOnSession is one enumerated session with a logged-on user.
type loggedOnSession struct {
	ID     uint32
	Active bool // WTSActive; otherwise disconnected
}

// sessionLogonAPI is the native half of the session logon over opaque token
// handles. Tests replace single functions.
type sessionLogonAPI struct {
	// sessions lists active and disconnected sessions, never session 0.
	sessions func() ([]loggedOnSession, error)
	// token is WTSQueryUserToken; errNoSessionUser when nobody is signed in.
	token func(id uint32) (io.Closer, error)
	// userSID is the token's user SID, read from the token, no name lookup.
	userSID func(tok io.Closer) (string, error)
	// filtered reports a UAC-filtered (limited) administrator token.
	filtered func(tok io.Closer) (bool, error)
	// linked returns the full token linked to a filtered one.
	linked func(tok io.Closer) (io.Closer, error)
	// duplicate makes a primary token that may change its session.
	duplicate func(tok io.Closer) (io.Closer, error)
	// setSession moves a primary token to session 0.
	setSession func(tok io.Closer) error
}

// orderSessions puts active sessions first, then disconnected ones, each by
// ascending ID.
func orderSessions(sessions []loggedOnSession) []loggedOnSession {
	out := append([]loggedOnSession(nil), sessions...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Active != out[j].Active {
			return out[i].Active
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// acquire selects a signed-in session of sid and prepares its token for a
// session-0 launch. selected reports whether a session of the account was
// found, so a failure belongs to that session. A non-nil token returned with
// an error holds handles that could not be closed: no fallback may follow and
// the caller must retry Close.
func (api sessionLogonAPI) acquire(sid string) (tok *UserToken, id uint32, selected bool, err error) {
	sessions, err := api.sessions()
	if err != nil {
		return nil, 0, false, fmt.Errorf("session enumeration: %w", err)
	}
	owner := &UserToken{}
	var source io.Closer
	var queryErr error
	for _, s := range orderSessions(sessions) {
		t, err := api.token(s.ID)
		if errors.Is(err, errNoSessionUser) {
			continue
		}
		if err != nil {
			if queryErr == nil {
				queryErr = fmt.Errorf("session %d: token: %w", s.ID, err)
			}
			continue
		}
		got, err := api.userSID(t)
		if err == nil && strings.EqualFold(got, sid) {
			source, id = t, s.ID
			break
		}
		if err != nil && queryErr == nil {
			queryErr = fmt.Errorf("session %d: token: %w", s.ID, err)
		}
		// Another user's token, or one whose user is unknown, is never used.
		if closeErr := t.Close(); closeErr != nil {
			owner.cleanup = append(owner.cleanup, t)
			return owner, 0, false, fmt.Errorf("session %d: close token: %w", s.ID, closeErr)
		}
	}
	if source == nil {
		if queryErr != nil {
			return nil, 0, false, queryErr
		}
		return nil, 0, false, errNoSignedInSession
	}
	owner.cleanup = append(owner.cleanup, source)
	tok, err = finishTokenAcquisition(owner, api.prepare(owner, source))
	if err != nil {
		err = fmt.Errorf("session %d: %w", id, err)
	}
	return tok, id, true, err
}

// prepare turns the session's token into owner's primary token in session 0.
// Every handle it obtains is owned by owner, the result as its native token.
func (api sessionLogonAPI) prepare(owner *UserToken, source io.Closer) error {
	use := source
	filtered, err := api.filtered(source)
	if err != nil {
		return fmt.Errorf("elevation type: %w", err)
	}
	if filtered {
		// The unit has the rights S4U gives an administrator: the full token.
		// The filtered token goes no further.
		full, err := api.linked(source)
		if full != nil {
			owner.cleanup = append(owner.cleanup, full)
		}
		if err != nil {
			return fmt.Errorf("linked token: %w", err)
		}
		use = full
	}
	primary, err := api.duplicate(use)
	if primary != nil {
		owner.native = primary
	}
	if err != nil {
		return fmt.Errorf("duplicate: %w", err)
	}
	if err := api.setSession(primary); err != nil {
		return fmt.Errorf("session id: %w", err)
	}
	return nil
}

// signedIn reports whether the account sid is still signed in to session id:
// the token query and the SID comparison, nothing else. A query that fails
// for another reason cannot tell, and counts as signed in.
func (api sessionLogonAPI) signedIn(id uint32, sid string) bool {
	t, err := api.token(id)
	if errors.Is(err, errNoSessionUser) {
		return false
	}
	if err != nil {
		return true
	}
	got, err := api.userSID(t)
	_ = t.Close()
	return err != nil || strings.EqualFold(got, sid)
}

// openAccountLogons applies the order of UserLogon=session-or-s4u: the
// session logon first, S4U only when it fails, in the same attempt. A failed
// session open that returned its session for a Close retry, or whose session
// signed out during the launch, is not followed by S4U.
func openAccountLogons(trySession bool, session, s4u func() (*AccountSession, error)) (*AccountSession, error) {
	if !trySession {
		return s4u()
	}
	s, sessionErr := session()
	if sessionErr == nil {
		return s, nil
	}
	var gone *sessionGoneError
	if s != nil || errors.As(sessionErr, &gone) {
		return s, sessionErr
	}
	s, s4uErr := s4u()
	if s4uErr != nil {
		return s, fmt.Errorf("%w; %w", sessionErr, s4uErr)
	}
	return s, nil
}
