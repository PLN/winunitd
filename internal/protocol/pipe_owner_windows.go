//go:build windows

package protocol

import (
	"fmt"
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

// verifyControlServerOwner refuses a system-pipe connection whose server
// process is not the documented system-manager identity: LocalSystem
// (token owner or user SID S-1-5-18) or Administrators (token owner
// S-1-5-32-544, or CheckTokenMembership). A lower-privileged squat is
// closed; no RPC is sent.
func verifyControlServerOwner(conn net.Conn) error {
	tok, err := namedPipeServerToken(conn)
	if err != nil {
		return fmt.Errorf("control pipe server identity: %w", err)
	}
	defer tok.Close()
	ok, got, err := controlDaemonIdentity(tok)
	if err != nil {
		return fmt.Errorf("control pipe server identity: %w", err)
	}
	if !ok {
		return fmt.Errorf("refusing control pipe: server owner SID %s is not LocalSystem or Administrators (possible squat)", got)
	}
	return nil
}

// verifyUserServerOwner refuses a user-pipe connection whose server
// process token user SID is not ownerSID (the user-manager identity for
// that pipe). A squat by any other account is closed; no RPC is sent.
func verifyUserServerOwner(conn net.Conn, ownerSID string) error {
	tok, err := namedPipeServerToken(conn)
	if err != nil {
		return fmt.Errorf("user pipe server identity: %w", err)
	}
	defer tok.Close()
	tu, err := tok.GetTokenUser()
	if err != nil {
		return fmt.Errorf("user pipe server identity: %w", err)
	}
	got := tu.User.Sid.String()
	if !sidEqual(got, ownerSID) {
		return fmt.Errorf("refusing user pipe: server process SID %s is not the user-manager identity %s (possible squat)", got, ownerSID)
	}
	return nil
}

func namedPipeServerToken(conn net.Conn) (windows.Token, error) {
	if conn == nil {
		return 0, fmt.Errorf("nil connection")
	}
	h, ok := connHandle(conn)
	if !ok {
		return 0, fmt.Errorf("connection is not a named pipe")
	}
	var pid uint32
	if err := windows.GetNamedPipeServerProcessId(h, &pid); err != nil {
		return 0, err
	}
	if pid == 0 {
		return 0, fmt.Errorf("named pipe server pid is 0")
	}
	proc, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(proc)

	var tok windows.Token
	if err := windows.OpenProcessToken(proc, windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &tok); err != nil {
		return 0, err
	}
	return tok, nil
}

func controlDaemonIdentity(tok windows.Token) (ok bool, owner string, err error) {
	ownerSID, err := tokenOwnerSID(tok)
	if err != nil {
		return false, "", err
	}
	owner = ownerSID.String()

	sysSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return false, owner, err
	}
	adminSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false, owner, err
	}
	if ownerSID.Equals(sysSID) || ownerSID.Equals(adminSID) {
		return true, owner, nil
	}

	tu, err := tok.GetTokenUser()
	if err != nil {
		return false, owner, err
	}
	if tu.User.Sid.Equals(sysSID) {
		return true, owner, nil
	}

	imp, err := duplicateImpersonation(tok)
	if err != nil {
		return false, owner, err
	}
	defer imp.Close()
	isAdmin, err := imp.IsMember(adminSID)
	if err != nil {
		return false, owner, err
	}
	return isAdmin, owner, nil
}

func tokenOwnerSID(tok windows.Token) (*windows.SID, error) {
	n := uint32(256)
	for {
		buf := make([]byte, n)
		err := windows.GetTokenInformation(tok, windows.TokenOwner, &buf[0], uint32(len(buf)), &n)
		if err == nil {
			p := *(*uintptr)(unsafe.Pointer(&buf[0]))
			if p == 0 {
				return nil, fmt.Errorf("token owner SID is nil")
			}
			return (*windows.SID)(unsafe.Pointer(p)).Copy()
		}
		if err != windows.ERROR_INSUFFICIENT_BUFFER {
			return nil, err
		}
		if n <= uint32(len(buf)) {
			return nil, err
		}
	}
}
