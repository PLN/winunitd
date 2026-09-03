//go:build windows

package protocol

import (
	"fmt"
	"net"
	"runtime"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

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

func pipeAuthorizer(ownerSID string) Authorizer {
	return func(conn net.Conn) (Peer, error) {
		return peerFromNamedPipe(conn, ownerSID)
	}
}

func peerFromNamedPipe(conn net.Conn, ownerSID string) (Peer, error) {
	if conn == nil {
		return Peer{}, fmt.Errorf("nil connection")
	}
	h, ok := connHandle(conn)
	if !ok {
		return Peer{}, fmt.Errorf("connection is not a named pipe")
	}
	p, err := peerFromClientProcess(h, ownerSID)
	if err == nil {
		return p, nil
	}
	p, err2 := peerFromImpersonation(h, ownerSID)
	if err2 == nil {
		return p, nil
	}
	return Peer{}, fmt.Errorf("client process token: %v; impersonation fallback: %w", err, err2)
}

func peerFromClientProcess(h windows.Handle, ownerSID string) (Peer, error) {
	var pid uint32
	if err := windows.GetNamedPipeClientProcessId(h, &pid); err != nil {
		return Peer{}, err
	}
	if pid == 0 {
		return Peer{}, fmt.Errorf("named pipe client pid is 0")
	}
	proc, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return Peer{}, err
	}
	defer windows.CloseHandle(proc)

	var tok windows.Token
	if err := windows.OpenProcessToken(proc, windows.TOKEN_QUERY, &tok); err != nil {
		return Peer{}, err
	}
	defer tok.Close()
	return peerFromToken(tok, ownerSID)
}

func peerFromImpersonation(h windows.Handle, ownerSID string) (Peer, error) {
	// Impersonation is the fallback when the client process token cannot
	// be opened. Lock the OS thread so RevertToSelf applies to the same
	// thread that impersonated; dispatch runs after revert.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := impersonateNamedPipeClient(h); err != nil {
		return Peer{}, err
	}
	defer windows.RevertToSelf()

	var tok windows.Token
	if err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &tok); err != nil {
		return Peer{}, err
	}
	defer tok.Close()
	return peerFromToken(tok, ownerSID)
}

func peerFromToken(tok windows.Token, ownerSID string) (Peer, error) {
	tu, err := tok.GetTokenUser()
	if err != nil {
		return Peer{}, err
	}
	sid := tu.User.Sid.String()
	p := Peer{SID: sid}

	sysSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return Peer{}, err
	}
	if tu.User.Sid.Equals(sysSID) {
		p.LocalSystem = true
	}

	adminSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return Peer{}, err
	}
	// CheckTokenMembership (Token.IsMember). A UAC-filtered token is
	// not Administrator: Administrators is deny-only on that token.
	isAdmin, err := tok.IsMember(adminSID)
	if err != nil {
		return Peer{}, err
	}
	p.Administrator = isAdmin

	if ownerSID != "" && sidEqual(sid, ownerSID) {
		p.Owner = true
	}
	return p, nil
}

func sidEqual(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	as, err1 := windows.StringToSid(a)
	bs, err2 := windows.StringToSid(b)
	if err1 != nil || err2 != nil {
		return strings.EqualFold(a, b)
	}
	return as.Equals(bs)
}

func connHandle(c net.Conn) (windows.Handle, bool) {
	type fder interface{ Fd() uintptr }
	if f, ok := c.(fder); ok {
		return windows.Handle(f.Fd()), true
	}
	sc, ok := c.(syscall.Conn)
	if !ok {
		return 0, false
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return 0, false
	}
	var h windows.Handle
	_ = raw.Control(func(fd uintptr) {
		h = windows.Handle(fd)
	})
	return h, h != 0
}

var procImpersonateNamedPipeClient = windows.NewLazySystemDLL("advapi32.dll").NewProc("ImpersonateNamedPipeClient")

func impersonateNamedPipeClient(h windows.Handle) error {
	r1, _, e1 := procImpersonateNamedPipeClient.Call(uintptr(h))
	if r1 == 0 {
		if e1 != syscall.Errno(0) {
			return e1
		}
		return syscall.EINVAL
	}
	return nil
}
