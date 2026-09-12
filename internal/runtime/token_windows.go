//go:build windows

package runtime

import (
	"golang.org/x/sys/windows"
)

type winToken windows.Token

func (t winToken) Close() error {
	if t == 0 {
		return nil
	}
	return windows.Token(t).Close()
}

// QueryUserToken calls WTSQueryUserToken for sessionID. There is no
// fallback if the call fails.
func QueryUserToken(sessionID uint32) (*UserToken, error) {
	var tok windows.Token
	if err := windows.WTSQueryUserToken(sessionID, &tok); err != nil {
		return nil, failClosed(sessionID, err)
	}
	info, err := userInfoFromToken(tok)
	if err != nil {
		_ = tok.Close()
		return nil, failClosed(sessionID, err)
	}
	return &UserToken{Info: info, native: winToken(tok)}, nil
}

// CurrentUserInfo reads identity from the current process token.
// Used by the user-manager process (already running as the user) and
// tests. The system manager's production path uses QueryUserToken.
func CurrentUserInfo() (UserInfo, error) {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &tok); err != nil {
		return UserInfo{}, err
	}
	defer tok.Close()
	return userInfoFromToken(tok)
}

func userInfoFromToken(tok windows.Token) (UserInfo, error) {
	tu, err := tok.GetTokenUser()
	if err != nil {
		return UserInfo{}, err
	}
	sid := tu.User.Sid.String()
	account, domain, _, err := tu.User.Sid.LookupAccount("")
	if err != nil {
		return UserInfo{}, err
	}
	profile, err := tok.GetUserProfileDirectory()
	if err != nil {
		return UserInfo{}, err
	}
	localAppData, err := tok.KnownFolderPath(windows.FOLDERID_LocalAppData, windows.KF_FLAG_DONT_VERIFY)
	if err != nil {
		return UserInfo{}, err
	}
	roamingAppData, err := tok.KnownFolderPath(windows.FOLDERID_RoamingAppData, windows.KF_FLAG_DONT_VERIFY)
	if err != nil {
		return UserInfo{}, err
	}
	return UserInfo{
		SID:            sid,
		Username:       account,
		Domain:         domain,
		Profile:        profile,
		LocalAppData:   localAppData,
		RoamingAppData: roamingAppData,
	}, nil
}

func nativeToken(t *UserToken) (windows.Token, bool) {
	if t == nil {
		return 0, false
	}
	wt, ok := t.native.(winToken)
	if !ok || wt == 0 {
		return 0, false
	}
	return windows.Token(wt), true
}
