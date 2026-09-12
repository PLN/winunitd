//go:build windows

package protocol

import (
	"errors"
	"fmt"
	"net"
	"runtime"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

func impersonatePeerPlatform(conn net.Conn, ownerSID string) (Peer, error) {
	h, ok := connHandle(conn)
	if !ok {
		return Peer{}, fmt.Errorf("connection is not a named pipe")
	}
	return peerFromImpersonation(h, ownerSID)
}

func clientProcessPeerPlatform(conn net.Conn, ownerSID string) (Peer, error) {
	h, ok := connHandle(conn)
	if !ok {
		return Peer{}, fmt.Errorf("connection is not a named pipe")
	}
	return peerFromClientProcess(h, ownerSID)
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
	if err := windows.OpenProcessToken(proc, windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &tok); err != nil {
		return Peer{}, err
	}
	defer tok.Close()
	return peerFromToken(tok, ownerSID)
}

func peerFromImpersonation(h windows.Handle, ownerSID string) (Peer, error) {
	return peerFromImpersonationWith(h, ownerSID, impersonateNamedPipeClient, peerFromThreadToken, windows.RevertToSelf)
}

func peerFromImpersonationWith(h windows.Handle, ownerSID string, impersonate func(windows.Handle) error, lookup func(string) (Peer, error), revert func() error) (Peer, error) {
	type result struct {
		peer Peer
		err  error
	}
	done := make(chan result, 1)
	// The bounded connection worker waits for this dedicated goroutine. The
	// caller never impersonates, and a failed revert retires only this thread.
	go func() {
		runtime.LockOSThread()
		unlock := true
		defer func() {
			if unlock {
				runtime.UnlockOSThread()
			}
		}()
		if err := impersonate(h); err != nil {
			done <- result{err: err}
			return
		}
		peer, err := lookup(ownerSID)
		if revertErr := revert(); revertErr != nil {
			unlock = false // retire the OS thread; it must never run another request
			err = errors.Join(err, fmt.Errorf("revert pipe impersonation: %w", revertErr))
		}
		if err != nil {
			done <- result{err: &impersonatedIdentityError{err: err}}
			return
		}
		done <- result{peer: peer}
	}()
	r := <-done
	return r.peer, r.err
}

func peerFromThreadToken(ownerSID string) (Peer, error) {
	var tok windows.Token
	if err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, true, &tok); err != nil {
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
	// ImpersonateNamedPipeClient already yields an impersonation token
	// (identification is enough). A process primary token (PID fallback)
	// fails CheckTokenMembership with ERROR_NO_IMPERSONATION_TOKEN and
	// needs DuplicateTokenEx first. Do not raise an identification token
	// to SecurityImpersonation — that fails ERROR_BAD_IMPERSONATION_LEVEL.
	isAdmin, err := tok.IsMember(adminSID)
	if err != nil {
		imp, err2 := duplicateImpersonation(tok)
		if err2 != nil {
			return Peer{}, err2
		}
		defer imp.Close()
		isAdmin, err = imp.IsMember(adminSID)
		if err != nil {
			return Peer{}, err
		}
	}
	// A UAC-filtered token is not Administrator: Administrators is deny-only.
	p.Administrator = isAdmin

	if ownerSID != "" && sidEqual(sid, ownerSID) {
		p.Owner = true
	}
	return p, nil
}

func duplicateImpersonation(tok windows.Token) (windows.Token, error) {
	var imp windows.Token
	err := windows.DuplicateTokenEx(tok, windows.TOKEN_QUERY, nil, windows.SecurityImpersonation, windows.TokenImpersonation, &imp)
	if err != nil {
		return 0, err
	}
	return imp, nil
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
