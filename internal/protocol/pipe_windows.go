//go:build windows

package protocol

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"
)

// ListenPipe creates a named pipe listener with ControlPipeSDDL.
func ListenPipe(name string) (net.Listener, error) {
	return winio.ListenPipe(name, &winio.PipeConfig{
		SecurityDescriptor: ControlPipeSDDL,
		InputBufferSize:    65536,
		OutputBufferSize:   65536,
	})
}

// ListenControl listens on DefaultPipeName.
func ListenControl() (net.Listener, error) {
	return ListenPipe(DefaultPipeName)
}

// DialPipe connects to a named pipe.
func DialPipe(ctx context.Context, name string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, name)
}

// DialDefault connects to DefaultPipeName.
func DialDefault(ctx context.Context) (net.Conn, error) {
	return DialPipe(ctx, DefaultPipeName)
}

// DefaultAuthorizer trusts the named-pipe ACL: a successful connect already
// means the client is LocalSystem or an Administrator.
func DefaultAuthorizer() Authorizer {
	return AllowAdmin
}
