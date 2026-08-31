//go:build !windows

package notify

import (
	"context"
	"fmt"
	"net"
	"strings"
)

// Listen is a fake TCP listener so manager tests stay green on Linux.
func Listen(unitID, sid string) (Listener, error) {
	_ = unitID
	_ = sid
	return ListenTCP()
}

// PipeSDDL is only meaningful on Windows.
func PipeSDDL(sid string) string {
	_ = sid
	return `D:P(A;;GA;;;SY)(A;;GA;;;BA)`
}

func dialAddr(ctx context.Context, addr string) (net.Conn, error) {
	if strings.HasPrefix(addr, `\\.\pipe\`) {
		return nil, pipeUnavailable(addr)
	}
	var d net.Dialer
	network := "tcp"
	if strings.HasPrefix(addr, "/") {
		network = "unix"
	}
	return d.DialContext(ctx, network, addr)
}

func pipeUnavailable(name string) error {
	return fmt.Errorf("named pipe %s is only available on Windows", name)
}
