//go:build !windows

package runtime

import "errors"

//lint:ignore U1000 Used by the Windows account session only.
type accountPlatform struct{}

// OpenAccountSession is only available on Windows.
func OpenAccountSession(AccountSessionSpec) (*AccountSession, error) {
	return nil, errors.New("User= requires Windows")
}

// Close does nothing on this build.
func (s *AccountSession) Close() error { return nil }
