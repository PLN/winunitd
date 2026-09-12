package runtime

import (
	"errors"
	"fmt"
	"io"
)

// ErrNoUserToken is returned when a user token cannot be obtained.
// Interactive logon uses WTSQueryUserToken only. Linger uses trusted-LSA
// S4U (and an optional named CredMan/LSA URI for network creds, via
// LOGON32_LOGON_BATCH). There is no password-in-file or password-in-env
// fallback.
var ErrNoUserToken = errors.New("no user token (fail closed)")

// UserToken owns a logon token and the identity needed for a deterministic
// environment. Interactive acquisition uses WTS; headless acquisition uses S4U.
type UserToken struct {
	Info UserInfo
	// SessionID is the selected interactive session; headless tokens use zero.
	SessionID uint32
	// Source is how a linger token was obtained: "s4u" or "store-uri".
	// Empty for WTS interactive tokens and test fakes.
	Source string
	// native is the OS token (Windows HANDLE). Callers outside this
	// package must not use it; StartUserManager borrows it during launch.
	native io.Closer
	// Auxiliary acquisition resources are owned even when no usable primary
	// token exists. Callers must close a non-nil token returned with an error.
	cleanup []io.Closer
}

// Close releases the token and auxiliary resources. Failure retains ownership
// for retry. Callers serialize Close with token use and other Close attempts.
func (t *UserToken) Close() error {
	if t == nil {
		return nil
	}
	var result error
	if t.native != nil {
		if err := t.native.Close(); err != nil {
			result = err
		} else {
			t.native = nil
		}
	}
	return errors.Join(result, t.closeAuxiliary())
}

func (t *UserToken) closeAuxiliary() error {
	var result error
	for i := len(t.cleanup) - 1; i >= 0; i-- {
		if c := t.cleanup[i]; c != nil {
			if err := c.Close(); err != nil {
				result = errors.Join(result, err)
			} else {
				t.cleanup[i] = nil
			}
		}
	}
	if result == nil {
		t.cleanup = nil
	}
	return result
}

type tokenCleanupFunc func() error

func (f tokenCleanupFunc) Close() error { return f() }

// No acquisition returns a usable token while temporary resources remain
// unresolved. A failure transfers only unfinished cleanup, never launch authority.
func finishTokenAcquisition(t *UserToken, err error) (*UserToken, error) {
	if t == nil {
		return nil, err
	}
	err = errors.Join(err, t.closeAuxiliary())
	if err == nil {
		return t, nil
	}
	if closeErr := t.Close(); closeErr != nil {
		return t, errors.Join(err, closeErr)
	}
	return nil, err
}

// Release one failed authentication attempt while keeping its connection and
// privilege-token resources. Failure stops further attempts and stays owned.
func closeTokenAttempt(t *UserToken, keep int) error {
	attempt := &UserToken{native: t.native, cleanup: t.cleanup[keep:]}
	err := attempt.Close()
	t.native = attempt.native
	t.cleanup = append(t.cleanup[:keep], attempt.cleanup...)
	return err
}

// TokenSource returns a user token for a session. The production
// implementation is WTSQueryUserToken only. A non-nil token returned with an
// error transfers unfinished cleanup ownership; callers must close it too.
type TokenSource func(sessionID uint32) (*UserToken, error)

func failClosed(sessionID uint32, err error) error {
	if err == nil {
		return fmt.Errorf("%w: session %d", ErrNoUserToken, sessionID)
	}
	return fmt.Errorf("%w: session %d: %v", ErrNoUserToken, sessionID, err)
}
