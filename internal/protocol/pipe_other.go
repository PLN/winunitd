//go:build !windows

package protocol

import (
	"context"
	"fmt"
	"net"
)

// ListenPipe is only available on Windows.
func ListenPipe(name string) (net.Listener, error) {
	return nil, fmt.Errorf("named pipe %s is only available on Windows", name)
}

// ListenControl is only available on Windows.
func ListenControl() (net.Listener, error) {
	return nil, fmt.Errorf("named pipe %s is only available on Windows", DefaultPipeName)
}

// DialPipe is only available on Windows.
func DialPipe(ctx context.Context, name string) (net.Conn, error) {
	return nil, fmt.Errorf("named pipe %s is only available on Windows", name)
}

// DialDefault is only available on Windows.
func DialDefault(ctx context.Context) (net.Conn, error) {
	return DialPipe(ctx, DefaultPipeName)
}

// DefaultAuthorizer denies all peers. Production control uses the Windows named pipe.
func DefaultAuthorizer() Authorizer {
	return DenyAll
}
