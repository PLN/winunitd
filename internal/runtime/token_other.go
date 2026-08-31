//go:build !windows

package runtime

import "fmt"

// QueryUserToken always fails closed off Windows. P1 has no stored-credential
// or alternate-logon fallback.
func QueryUserToken(sessionID uint32) (*UserToken, error) {
	return nil, failClosed(sessionID, fmt.Errorf("WTSQueryUserToken is only available on Windows"))
}

// CurrentUserInfo is only available on Windows.
func CurrentUserInfo() (UserInfo, error) {
	return UserInfo{}, fmt.Errorf("current user info is only available on Windows")
}
