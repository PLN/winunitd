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

// UserToken is a logon token for one interactive session plus the
// identity needed for a deterministic environment. Production obtains
// it only via WTSQueryUserToken.
type UserToken struct {
	Info UserInfo
	// Source is how a linger token was obtained: "s4u" or "store-uri".
	// Empty for WTS interactive tokens and test fakes.
	Source string
	// native is the OS token (Windows HANDLE). Callers outside this
	// package must not use it; StartUserManager consumes it.
	native io.Closer
}

// Close releases the native token handle. Failure retains ownership for retry.
func (t *UserToken) Close() error {
	if t == nil || t.native == nil {
		return nil
	}
	if err := t.native.Close(); err != nil {
		return err
	}
	t.native = nil
	return nil
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
