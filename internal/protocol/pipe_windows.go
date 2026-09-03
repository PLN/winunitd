//go:build windows

package protocol

import (
	"context"
	"fmt"
	"net"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func listenPipeSDDL(name, sddl string) (net.Listener, error) {
	return winio.ListenPipe(name, &winio.PipeConfig{
		SecurityDescriptor: sddl,
		InputBufferSize:    65536,
		OutputBufferSize:   65536,
	})
}

// ListenPipe creates a named pipe listener with ControlPipeSDDL.
func ListenPipe(name string) (net.Listener, error) {
	return listenPipeSDDL(name, ControlPipeSDDL)
}

// ListenPipeSDDL creates a named pipe listener with the given SDDL.
func ListenPipeSDDL(name, sddl string) (net.Listener, error) {
	if sddl == "" {
		return nil, fmt.Errorf("security descriptor required")
	}
	return listenPipeSDDL(name, sddl)
}

// ListenControl listens on DefaultPipeName.
func ListenControl() (net.Listener, error) {
	return ListenPipe(DefaultPipeName)
}

// ListenUserControl listens on the per-user pipe for sid.
func ListenUserControl(sid string) (net.Listener, error) {
	sddl, err := UserPipeSDDL(sid)
	if err != nil {
		return nil, err
	}
	return listenPipeSDDL(UserPipeName(sid), sddl)
}

// DialPipe connects to a named pipe.
func DialPipe(ctx context.Context, name string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, name)
}

// DialDefault connects to DefaultPipeName.
func DialDefault(ctx context.Context) (net.Conn, error) {
	return DialPipe(ctx, DefaultPipeName)
}

// DialUser connects to the per-user control pipe for sid.
func DialUser(ctx context.Context, sid string) (net.Conn, error) {
	if !ValidSID(sid) {
		return nil, fmt.Errorf("invalid SID %q", sid)
	}
	return DialPipe(ctx, UserPipeName(sid))
}

// CurrentUserSID is the SID of the current process token.
func CurrentUserSID() (string, error) {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &tok); err != nil {
		return "", err
	}
	defer tok.Close()
	tu, err := tok.GetTokenUser()
	if err != nil {
		return "", err
	}
	return tu.User.Sid.String(), nil
}
