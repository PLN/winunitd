package protocol

import (
	"net"
)

// Peer is the authenticated identity of a control connection.
// Production Windows fills this from the named-pipe client token
// (DESIGN.md §30). The pipe DACL is defense-in-depth, not the auth model.
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
