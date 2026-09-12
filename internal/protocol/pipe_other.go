//go:build !windows

package protocol

import (
	"context"
	"fmt"
	"net"
)

func pipeUnavailable(name string) error {
	return fmt.Errorf("named pipe %s is only available on Windows", name)
}

// ListenPipe is only available on Windows.
func ListenPipe(name string) (net.Listener, error) {
	return nil, pipeUnavailable(name)
}

// ListenPipeSDDL is only available on Windows.
func ListenPipeSDDL(name, sddl string) (net.Listener, error) {
	_ = sddl
	return nil, pipeUnavailable(name)
}

// ListenControl is only available on Windows.
func ListenControl() (net.Listener, error) {
	return nil, pipeUnavailable(DefaultPipeName)
}

// ListenUserControl is only available on Windows.
func ListenUserControl(sid string) (net.Listener, error) {
	return nil, pipeUnavailable(UserPipeName(sid))
}

// DialPipe is only available on Windows.
func DialPipe(ctx context.Context, name string) (net.Conn, error) {
	_ = ctx
	return nil, pipeUnavailable(name)
}

// DialDefault is only available on Windows.
func DialDefault(ctx context.Context) (net.Conn, error) {
	return DialPipe(ctx, DefaultPipeName)
}

func DialMaintenance(ctx context.Context) (net.Conn, error) {
	return DialPipe(ctx, MaintenancePipeName)
}

// DialUser is only available on Windows.
func DialUser(ctx context.Context, sid string) (net.Conn, error) {
	return DialPipe(ctx, UserPipeName(sid))
}

// CurrentUserSID is only available on Windows.
func CurrentUserSID() (string, error) {
	return "", fmt.Errorf("current user SID is only available on Windows")
}
