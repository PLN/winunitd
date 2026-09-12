package protocol

import (
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
)

// Peer is the authenticated identity of a control connection.
// Production Windows fills this from the named-pipe client token
// (DESIGN.md §30): ImpersonateNamedPipeClient first; GetNamedPipeClientProcessId
// only if impersonation fails. The pipe DACL is defense-in-depth, not the
// auth model.
type Peer struct {
	SID           string
	LocalSystem   bool
	Administrator bool
	// Owner is the user whose SID the user-manager pipe is keyed on.
	Owner bool
}

// Allowed reports whether the peer may use the control API.
// DESIGN.md §30: administrators, local system, and the owning user on
// a user-manager pipe. A SID alone does not grant access.
func (p Peer) Allowed() bool {
	return p.LocalSystem || p.Administrator || p.Owner
}

// CanLinger reports whether the peer may enable-linger / disable-linger.
// Only Administrators (and LocalSystem) may; Owner-only fails closed.
func (p Peer) CanLinger() bool {
	return p.Administrator || p.LocalSystem
}

// Authorizer identifies the peer on a control connection.
type Authorizer func(conn net.Conn) (Peer, error)

// AllowAdmin treats every connection as an administrator. Tests use this
// with a fake listener. Production Windows uses DefaultAuthorizer (client
// token). Do not use AllowAdmin in production.
func AllowAdmin(_ net.Conn) (Peer, error) {
	return Peer{Administrator: true}, nil
}

// AllowOwner treats every connection as the pipe owner. Tests use this
// for user-manager pipes. Production Windows uses UserAuthorizer.
func AllowOwner(_ net.Conn) (Peer, error) {
	return Peer{Owner: true}, nil
}

// AllowLocalSystem treats every connection as LocalSystem.
func AllowLocalSystem(_ net.Conn) (Peer, error) {
	return Peer{LocalSystem: true}, nil
}

// DenyAll treats every connection as unauthorized.
func DenyAll(_ net.Conn) (Peer, error) {
	return Peer{}, nil
}

func authorize(auth Authorizer, conn net.Conn) (Peer, error) {
	if auth == nil {
		return Peer{}, nil
	}
	return auth(conn)
}

// DefaultAuthorizer derives Peer from the named-pipe client token.
// It does not stamp Administrator on every connection.
func DefaultAuthorizer() Authorizer {
	return pipeAuthorizer("")
}

// UserAuthorizer is DefaultAuthorizer plus Owner when the token SID
// matches ownerSID (the user-manager pipe key).
func UserAuthorizer(ownerSID string) Authorizer {
	return pipeAuthorizer(ownerSID)
}

type peerLookup func(conn net.Conn, ownerSID string) (Peer, error)

// Once impersonation succeeded, a token-query or revert failure must not fall
// back to the opener process identity, which can differ from its client token.
type impersonatedIdentityError struct{ err error }

func (e *impersonatedIdentityError) Error() string { return e.err.Error() }
func (e *impersonatedIdentityError) Unwrap() error { return e.err }

func pipeAuthorizer(ownerSID string) Authorizer {
	return authorizerWithLookups(ownerSID, impersonatePeerPlatform, clientProcessPeerPlatform)
}

// authorizerWithLookups is the production A1 path with injectable lookups.
// Tests use it to return admin vs non-admin Peers from a fake impersonation
// hook without a real pipe, and to prove PID is only a fallback.
func authorizerWithLookups(ownerSID string, impersonate, pid peerLookup) Authorizer {
	return func(conn net.Conn) (Peer, error) {
		return peerFromLookups(conn, ownerSID, impersonate, pid)
	}
}

func peerFromLookups(conn net.Conn, ownerSID string, impersonate, pid peerLookup) (Peer, error) {
	if conn == nil {
		return Peer{}, fmt.Errorf("nil connection")
	}
	if impersonate == nil {
		impersonate = impersonatePeerPlatform
	}
	if pid == nil {
		pid = clientProcessPeerPlatform
	}
	p, err := impersonate(conn, ownerSID)
	if err == nil {
		return p, nil
	}
	var identityErr *impersonatedIdentityError
	if errors.As(err, &identityErr) {
		return Peer{}, err
	}
	p, err2 := pid(conn, ownerSID)
	if err2 == nil {
		logAuthf("winunitd: authorizer: impersonation failed, using client process token: %v", err)
		return p, nil
	}
	return Peer{}, fmt.Errorf("impersonation: %v; client process token: %w", err, err2)
}

var (
	authLogMu sync.Mutex
	authLog   = func(format string, args ...any) {
		log.Printf(format, args...)
	}
)

func logAuthf(format string, args ...any) {
	authLogMu.Lock()
	f := authLog
	authLogMu.Unlock()
	f(format, args...)
}
