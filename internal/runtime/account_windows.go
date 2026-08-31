//go:build windows

package runtime

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// LookupAccountName resolves a username (DOMAIN\user or user) or SID
// string to UserInfo. Profile is filled when the account's profile
// directory can be derived; token-based lookup is preferred at linger
// start after S4U.
func LookupAccountName(name string) (UserInfo, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return UserInfo{}, fmt.Errorf("account name required")
	}
	if validAccountSID(name) {
		sid, err := windows.StringToSid(name)
		if err != nil {
			return UserInfo{}, fmt.Errorf("lookup SID %s: %w", name, err)
		}
		account, domain, _, err := sid.LookupAccount("")
		if err != nil {
			return UserInfo{SID: name}, nil
		}
		return UserInfo{SID: name, Username: account, Domain: domain}, nil
	}
	sid, domain, _, err := windows.LookupSID("", name)
	if err != nil {
		return UserInfo{}, fmt.Errorf("lookup account %q: %w", name, err)
	}
	sidStr := sid.String()
	account, accDomain, _, err := sid.LookupAccount("")
	if err != nil {
		account = name
		accDomain = domain
		if i := strings.LastIndex(name, `\`); i >= 0 {
			account = name[i+1:]
			accDomain = name[:i]
		}
	}
	return UserInfo{SID: sidStr, Username: account, Domain: accDomain}, nil
}

// SIDHasInteractiveSession reports whether sid currently has a suitable
// interactive session (active, connected, or disconnected; not session 0).
func SIDHasInteractiveSession(sid string) bool {
	if !validAccountSID(sid) {
		return false
	}
	ids, err := InteractiveSessions()
	if err != nil {
		return false
	}
	for _, id := range ids {
		got, err := sessionUserSID(id)
		if err != nil {
			continue
		}
		if strings.EqualFold(got, sid) {
			return true
		}
	}
	return false
}

var (
	modWtsapi32                     = windows.NewLazySystemDLL("wtsapi32.dll")
	procWTSQuerySessionInformationW = modWtsapi32.NewProc("WTSQuerySessionInformationW")
)

const (
	wtsUserName   = 5
	wtsDomainName = 7
)

func sessionUserSID(sessionID uint32) (string, error) {
	user, err := querySessionString(sessionID, wtsUserName)
	if err != nil {
		return "", err
	}
	domain, err := querySessionString(sessionID, wtsDomainName)
	if err != nil {
		return "", err
	}
	user = strings.TrimSpace(user)
	domain = strings.TrimSpace(domain)
	if user == "" {
		return "", fmt.Errorf("session %d: empty username", sessionID)
	}
	name := user
	if domain != "" {
		name = domain + `\` + user
	}
	info, err := LookupAccountName(name)
	if err != nil {
		return "", err
	}
	return info.SID, nil
}

func querySessionString(sessionID uint32, class uint32) (string, error) {
	var buf *uint16
	var size uint32
	r1, _, e1 := procWTSQuerySessionInformationW.Call(
		0,
		uintptr(sessionID),
		uintptr(class),
		uintptr(unsafe.Pointer(&buf)),
		uintptr(unsafe.Pointer(&size)),
	)
	if r1 == 0 {
		if e1 != syscall.Errno(0) {
			return "", e1
		}
		return "", fmt.Errorf("WTSQuerySessionInformation class %d failed", class)
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(buf)))
	return windows.UTF16PtrToString(buf), nil
}
