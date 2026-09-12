//go:build windows

package runtime

import (
	"fmt"
	"io"
	"os"
	goruntime "runtime"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var profileDLL = windows.NewLazySystemDLL("userenv.dll")
var loadProfileProc = profileDLL.NewProc("LoadUserProfileW")
var unloadProfileProc = profileDLL.NewProc("UnloadUserProfile")

type userProfileInfo struct {
	size, flags                                 uint32
	username, path, defaultPath, server, policy *uint16
	profile                                     windows.Handle
}

type userProfileLease struct {
	mu      sync.Mutex
	token   windows.Token
	profile windows.Handle
}

func (p *userProfileLease) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.profile != 0 {
		ok, _, err := unloadProfileProc.Call(uintptr(p.token), uintptr(p.profile))
		if ok == 0 {
			return fmt.Errorf("UnloadUserProfile: %w", err)
		}
		p.profile = 0 // UnloadUserProfile closes this key; never RegCloseKey it.
	}
	if p.token != 0 {
		if err := p.token.Close(); err != nil {
			return fmt.Errorf("close profile token: %w", err)
		}
		p.token = 0
	}
	return nil
}

func loadUserManagerProfile(tok windows.Token, sid string) (io.Closer, error) {
	identity, err := tok.GetTokenUser()
	if err != nil {
		return nil, err
	}
	if identity.User.Sid.String() != sid {
		return nil, fmt.Errorf("profile token SID mismatch")
	}
	username, domain, _, err := identity.User.Sid.LookupAccount("")
	if err != nil {
		return nil, err
	}
	computer, err := os.Hostname()
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(domain, computer) {
		return nil, fmt.Errorf("managed profile loading currently requires a local machine account")
	}
	name, err := windows.UTF16PtrFromString(username)
	if err != nil {
		return nil, err
	}
	if err := enableProfilePrivileges(); err != nil {
		return nil, err
	}
	lease := &userProfileLease{}
	if err := windows.DuplicateTokenEx(tok, windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE|windows.TOKEN_IMPERSONATE,
		nil, windows.SecurityImpersonation, windows.TokenPrimary, &lease.token); err != nil {
		return nil, err
	}
	info := userProfileInfo{flags: 1, username: name} // PI_NOUI: service work must never show dialogs
	info.size = uint32(unsafe.Sizeof(info))
	ok, _, callErr := loadProfileProc.Call(uintptr(lease.token), uintptr(unsafe.Pointer(&info)))
	goruntime.KeepAlive(name)
	goruntime.KeepAlive(info)
	if ok == 0 {
		return lease, fmt.Errorf("LoadUserProfile: %w", callErr)
	}
	lease.profile = info.profile
	return lease, nil
}

func enableProfilePrivileges() error {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_ADJUST_PRIVILEGES, &token); err != nil {
		return err
	}
	defer token.Close()
	for _, privilege := range []string{"SeBackupPrivilege", "SeRestorePrivilege"} {
		name, _ := windows.UTF16PtrFromString(privilege)
		var luid windows.LUID
		if err := windows.LookupPrivilegeValue(nil, name, &luid); err != nil {
			return err
		}
		state := windows.Tokenprivileges{PrivilegeCount: 1, Privileges: [1]windows.LUIDAndAttributes{{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED}}}
		if err := windows.AdjustTokenPrivileges(token, false, &state, 0, nil, nil); err != nil {
			return err
		}
	}
	return nil
}
