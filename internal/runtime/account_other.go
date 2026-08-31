//go:build !windows

package runtime

import "fmt"

// LookupAccountName is only available on Windows.
func LookupAccountName(name string) (UserInfo, error) {
	if validAccountSID(name) {
		return UserInfo{SID: name}, nil
	}
	return UserInfo{}, fmt.Errorf("account lookup is only available on Windows")
}

// SIDHasInteractiveSession is false off Windows.
func SIDHasInteractiveSession(sid string) bool {
	_ = sid
	return false
}
