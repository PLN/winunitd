package runtime

import (
	"errors"
	"fmt"
	"io"
)

// ErrNoUserToken is returned when WTSQueryUserToken cannot produce a
// token. P1 fails closed: no stored-credential or alternate-logon fallback.
var ErrNoUserToken = errors.New("no user token (WTSQueryUserToken failed; fail closed)")

// UserToken is a logon token for one interactive session plus the
// identity needed for a deterministic environment. Production obtains
// it only via WTSQueryUserToken.
type UserToken struct {
	Info UserInfo
	// native is the OS token (Windows HANDLE). Callers outside this
	// package must not use it; StartUserManager consumes it.
	native io.Closer
}

// Close releases the native token handle.
func (t *UserToken) Close() error {
	if t == nil || t.native == nil {
		return nil
	}
	err := t.native.Close()
	t.native = nil
	return err
}

// TokenSource returns a user token for a session. The production
// implementation is WTSQueryUserToken only.
type TokenSource func(sessionID uint32) (*UserToken, error)

func failClosed(sessionID uint32, err error) error {
	if err == nil {
		return fmt.Errorf("%w: session %d", ErrNoUserToken, sessionID)
	}
	return fmt.Errorf("%w: session %d: %v", ErrNoUserToken, sessionID, err)
}
