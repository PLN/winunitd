//go:build !windows

package protocol

import (
	"fmt"
	"net"
)

func impersonatePeerPlatform(conn net.Conn, ownerSID string) (Peer, error) {
	_ = conn
	_ = ownerSID
	return Peer{}, fmt.Errorf("named-pipe impersonation is only available on Windows")
}

func clientProcessPeerPlatform(conn net.Conn, ownerSID string) (Peer, error) {
	_ = conn
	_ = ownerSID
	return Peer{}, fmt.Errorf("named-pipe client process token is only available on Windows")
}
