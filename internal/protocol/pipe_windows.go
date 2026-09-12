//go:build windows

package protocol

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func listenPipeSDDL(name, sddl string) (net.Listener, error) {
	// go-winio ListenPipe creates the first instance with NT FILE_CREATE
	// (Win32 FILE_FLAG_FIRST_PIPE_INSTANCE: the name must not already
	// exist) and FILE_PIPE_REJECT_REMOTE_CLIENTS on every instance.
	lis, err := winio.ListenPipe(name, &winio.PipeConfig{
		SecurityDescriptor: sddl,
		InputBufferSize:    65536,
		OutputBufferSize:   65536,
	})
	if err != nil {
		return nil, firstInstanceError(name, err)
	}
	return lis, nil
}

func firstInstanceError(name string, err error) error {
	if isPipeNameTaken(err) {
		return fmt.Errorf("named pipe %s already taken (first instance only): %w", name, err)
	}
	return err
}

func isPipeNameTaken(err error) bool {
	if err == nil {
		return false
	}
	// go-winio uses NT FILE_CREATE → ERROR_ALREADY_EXISTS.
	// Win32 FILE_FLAG_FIRST_PIPE_INSTANCE → ERROR_ACCESS_DENIED.
	return errors.Is(err, windows.ERROR_ALREADY_EXISTS) ||
		errors.Is(err, windows.ERROR_FILE_EXISTS) ||
		errors.Is(err, windows.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windows.ERROR_PIPE_BUSY) ||
		errors.Is(err, syscall.EEXIST)
}

// ListenPipe creates a named pipe listener with ControlPipeSDDL,
// PIPE_REJECT_REMOTE_CLIENTS, and first-instance-only create.
func ListenPipe(name string) (net.Listener, error) {
	return listenPipeSDDL(name, ControlPipeSDDL)
}

// ListenPipeSDDL creates a named pipe listener with the given SDDL,
// PIPE_REJECT_REMOTE_CLIENTS, and first-instance-only create.
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

// DialPipe connects to a named pipe at PipeDialImpLevel (identification).
// It does not check the server owner; DialDefault and DialUser do
// (squat defense). Notify uses this path.
func DialPipe(ctx context.Context, name string) (net.Conn, error) {
	return winio.DialPipeAccessImpLevel(ctx, name,
		uint32(windows.GENERIC_READ|windows.GENERIC_WRITE),
		winio.PipeImpLevel(PipeDialImpLevel))
}

// DialDefault connects to DefaultPipeName and refuses the connection
// unless the server process is the system-manager identity (LocalSystem
// or Administrators).
func DialDefault(ctx context.Context) (net.Conn, error) {
	return dialVerified(ctx, DefaultPipeName, verifyControlServerOwner)
}

// DialMaintenance applies the same server identity checks as system control.
func DialMaintenance(ctx context.Context) (net.Conn, error) {
	return dialVerified(ctx, MaintenancePipeName, verifyControlServerOwner)
}

// DialUser connects to the per-user control pipe for sid and refuses
// unless the server process token user SID is that user-manager identity.
func DialUser(ctx context.Context, sid string) (net.Conn, error) {
	if !ValidSID(sid) {
		return nil, fmt.Errorf("invalid SID %q", sid)
	}
	return dialVerified(ctx, UserPipeName(sid), func(conn net.Conn) error {
		return verifyUserServerOwner(conn, sid)
	})
}

func dialVerified(ctx context.Context, name string, verify func(net.Conn) error) (net.Conn, error) {
	conn, err := DialPipe(ctx, name)
	if err != nil {
		return nil, err
	}
	if err := verify(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
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
