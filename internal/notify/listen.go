package notify

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
)

// Conn is one accepted notify client. ClientPID is 0 when unknown
// (fake TCP listener; Linux tests).
type Conn interface {
	net.Conn
	ClientPID() int
}

// Listener accepts notify clients and reports the address injected as
// WINUNIT_NOTIFY_PIPE.
type Listener interface {
	Addr() string
	Accept() (Conn, error)
	Close() error
}

// ListenFunc opens a per-unit notify listener. A non-nil listener returned
// with an error still belongs to the caller and must be closed.
type ListenFunc func(unitID string) (Listener, error)

type wrapConn struct {
	net.Conn
	pid int
}

func (c wrapConn) ClientPID() int { return c.pid }

// tcpListener is the fake listener used on Linux and in portable tests.
type tcpListener struct {
	ln   net.Listener
	addr string
}

// ListenTCP binds 127.0.0.1:0. WINUNIT_NOTIFY_PIPE is host:port.
func ListenTCP() (Listener, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	return &tcpListener{ln: ln, addr: ln.Addr().String()}, nil
}

func (l *tcpListener) Addr() string { return l.addr }

func (l *tcpListener) Accept() (Conn, error) {
	c, err := l.ln.Accept()
	if err != nil {
		return nil, err
	}
	return wrapConn{Conn: c, pid: 0}, nil
}

func (l *tcpListener) Close() error {
	if l == nil || l.ln == nil {
		return nil
	}
	return l.ln.Close()
}

// ServeAccept loops Accept until the listener is closed and delivers
// parsed messages. access, if non-nil, may reject a connection (NotifyAccess).
// Cancellation closes the listener and accepted connections so idle clients
// cannot prevent shutdown. Returning also cancels outstanding client reads.
func ServeAccept(ctx context.Context, lis Listener, access func(pid int) bool, emit func(Message)) {
	if lis == nil || emit == nil {
		return
	}
	var wg sync.WaitGroup
	defer wg.Wait()
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopListener := context.AfterFunc(ctx, func() { _ = lis.Close() })
	defer stopListener()
	for {
		if ctx != nil && ctx.Err() != nil {
			return
		}
		c, err := lis.Accept()
		if err != nil {
			return
		}
		if access != nil && !access(c.ClientPID()) {
			_ = c.Close()
			continue
		}
		wg.Add(1)
		go func(c Conn) {
			defer wg.Done()
			defer c.Close()
			stopRead := context.AfterFunc(ctx, func() { _ = c.Close() })
			defer stopRead()
			if _, err := io.WriteString(c, acceptanceBanner); err != nil {
				return
			}
			_ = ReadLines(c, emit)
		}(c)
	}
}

// Dial connects to a notify pipe address: a Windows named pipe, a TCP
// host:port (fake listener), or a unix socket path.
func Dial(ctx context.Context, addr string) (net.Conn, error) {
	if addr == "" {
		return nil, fmt.Errorf("%s is empty", EnvNotifyPipe)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return dialAddr(ctx, addr)
}
