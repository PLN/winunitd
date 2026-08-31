//go:build windows

package notify

import (
	"context"
	"net"
	"strings"
	"syscall"
	"unsafe"

	"github.com/PLN/winunitd/internal/protocol"
	"golang.org/x/sys/windows"
)

// Listen opens \\.\pipe\winunitd\notify\<unit-id> with a DACL for the unit
// process user (sid, or the current process user) plus LocalSystem and
// Administrators (DESIGN.md §19.1).
func Listen(unitID, sid string) (Listener, error) {
	if sid == "" {
		if got, err := protocol.CurrentUserSID(); err == nil {
			sid = got
		}
	}
	name := PipeName(unitID)
	sddl := PipeSDDL(sid)
	ln, err := protocol.ListenPipeSDDL(name, sddl)
	if err != nil {
		return nil, err
	}
	return &pipeListener{name: name, ln: ln}, nil
}

// PipeSDDL allows the unit user, LocalSystem, and Administrators.
func PipeSDDL(sid string) string {
	if protocol.ValidSID(sid) {
		return "D:P(A;;GA;;;" + sid + ")(A;;GA;;;SY)(A;;GA;;;BA)"
	}
	return protocol.ControlPipeSDDL
}

type pipeListener struct {
	name string
	ln   net.Listener
}

func (l *pipeListener) Addr() string { return l.name }

func (l *pipeListener) Accept() (Conn, error) {
	c, err := l.ln.Accept()
	if err != nil {
		return nil, err
	}
	return wrapConn{Conn: c, pid: namedPipeClientPID(c)}, nil
}

func (l *pipeListener) Close() error {
	if l == nil || l.ln == nil {
		return nil
	}
	return l.ln.Close()
}

func dialAddr(ctx context.Context, addr string) (net.Conn, error) {
	if strings.HasPrefix(addr, `\\.\pipe\`) || strings.HasPrefix(strings.ToLower(addr), `\\.\pipe\`) {
		return protocol.DialPipe(ctx, addr)
	}
	var d net.Dialer
	network := "tcp"
	if strings.HasPrefix(addr, "/") || strings.Contains(addr, `\`) && !strings.Contains(addr, ":") {
		network = "unix"
	}
	return d.DialContext(ctx, network, addr)
}

var procGetNamedPipeClientProcessId = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetNamedPipeClientProcessId")

func namedPipeClientPID(c net.Conn) int {
	h, ok := connHandle(c)
	if !ok || h == 0 {
		return 0
	}
	var pid uint32
	r1, _, _ := procGetNamedPipeClientProcessId.Call(uintptr(h), uintptr(unsafe.Pointer(&pid)))
	if r1 == 0 {
		return 0
	}
	return int(pid)
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
