//go:build !windows

package manager

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/notify"
)

// notifyListenSeq names every test notify listener in this process once.
var notifyListenSeq atomic.Uint64

// testNotifyListen opens manager-test notify listeners on Unix socket paths
// that are never reused. The portable TCP listener reports no client PID, so
// any loopback client is accepted, and a closed listener's port can be handed
// to a later listener while another test still resends READY=1 to it. A path
// in a directory removed on Close cannot be bound again, so a stale sender
// cannot complete a later invocation's readiness.
func testNotifyListen(t *testing.T) notify.ListenFunc {
	t.Helper()
	base, err := os.MkdirTemp("", "wun")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	return func(string) (notify.Listener, error) {
		dir := filepath.Join(base, strconv.FormatUint(notifyListenSeq.Add(1), 10))
		if err := os.Mkdir(dir, 0o700); err != nil {
			return nil, err
		}
		ln, err := net.Listen("unix", filepath.Join(dir, "s"))
		if err != nil {
			_ = os.RemoveAll(dir)
			return nil, err
		}
		return &unixNotifyListener{ln: ln, dir: dir}, nil
	}
}

type unixNotifyListener struct {
	ln  net.Listener
	dir string
}

func (l *unixNotifyListener) Addr() string { return l.ln.Addr().String() }

func (l *unixNotifyListener) Accept() (notify.Conn, error) {
	c, err := l.ln.Accept()
	if err != nil {
		return nil, err
	}
	return unixNotifyConn{Conn: c}, nil
}

func (l *unixNotifyListener) Close() error {
	err := l.ln.Close()
	_ = os.RemoveAll(l.dir)
	return err
}

// unixNotifyConn reports no client PID, as the portable TCP listener does.
type unixNotifyConn struct{ net.Conn }

func (unixNotifyConn) ClientPID() int { return 0 }

// The kernel can hand a closed loopback port to the next listener; a closed
// test notify address must not be bindable at all, so READY=1 resent to it by a
// finished invocation's sender cannot reach a later one (#277).
func TestNotifyTestListenerAddressIsNotReused(t *testing.T) {
	t.Parallel()
	listen := testNotifyListen(t)
	first, err := listen("first")
	if err != nil {
		t.Fatal(err)
	}
	addr := first.Addr()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	network := "tcp"
	if strings.HasPrefix(addr, "/") {
		network = "unix"
	}
	if ln, err := net.Listen(network, addr); err == nil {
		_ = ln.Close()
		t.Fatalf("closed notify address %s can be bound again", addr)
	}
	later, err := listen("later")
	if err != nil {
		t.Fatal(err)
	}
	defer later.Close()
	if later.Addr() == addr {
		t.Fatalf("later listener reuses %s", addr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := notify.Send(ctx, addr, notify.Message{Ready: true}); err == nil {
		t.Fatalf("READY=1 to closed notify address %s was delivered", addr)
	}
}
