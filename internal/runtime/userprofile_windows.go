//go:build windows

package runtime

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

type userProfileLease struct {
	mu          sync.Mutex
	interactive registry.Key
}

func (p *userProfileLease) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.interactive != 0 {
		if err := p.interactive.Close(); err != nil {
			return fmt.Errorf("close interactive profile key: %w", err)
		}
		p.interactive = 0
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
	_, domain, _, err := identity.User.Sid.LookupAccount("")
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
	var session, returned uint32
	if err := windows.GetTokenInformation(tok, windows.TokenSessionId, (*byte)(unsafe.Pointer(&session)), uint32(unsafe.Sizeof(session)), &returned); err != nil {
		return nil, fmt.Errorf("query profile session: %w", err)
	}
	if session == 0 {
		return nil, fmt.Errorf("headless profiles require Windows-owned process launch")
	}
	// Windows owns interactive profile lifetime. Only retain an ordinary registry
	// handle, which Windows closes on broker death. Never manually LoadUserProfile:
	// that reference can survive abrupt death and prevent final logoff unloading.
	key, err := registry.OpenKey(registry.USERS, sid, registry.READ)
	if err != nil {
		return nil, fmt.Errorf("interactive user profile is unavailable: %w", err)
	}
	return &userProfileLease{interactive: key}, nil
}
