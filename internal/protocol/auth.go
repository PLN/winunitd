package protocol

import (
	"net"
)

// Peer is the authenticated identity of a control connection.
type Peer struct {
	LocalSystem   bool
	Administrator bool
}

// Allowed reports whether the peer may use the control API.
// DESIGN.md §30: administrators, local system (user managers later).
func (p Peer) Allowed() bool {
	return p.LocalSystem || p.Administrator
}

// Authorizer identifies the peer on a control connection.
type Authorizer func(conn net.Conn) (Peer, error)

// AllowAdmin treats every connection as an administrator. Tests use this
// with a fake listener; production Windows uses the named-pipe ACL plus
// DefaultAuthorizer.
func AllowAdmin(_ net.Conn) (Peer, error) {
	return Peer{Administrator: true}, nil
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
