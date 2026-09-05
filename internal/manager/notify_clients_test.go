package manager

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/notify"
)

type failingNotifyConn struct {
	net.Conn
	fail  atomic.Bool
	calls atomic.Int32
}

func (c *failingNotifyConn) ClientPID() int { return 0 }
func (c *failingNotifyConn) Close() error {
	c.calls.Add(1)
	if c.fail.Load() {
		return errors.New("client close failed")
	}
	return c.Conn.Close()
}

type oneNotifyListener struct {
	controlledNotifyListener
	conn notify.Conn
}

func (l *oneNotifyListener) Accept() (notify.Conn, error) { return l.conn, nil }

func TestNotificationClientCloseFailureRemainsOwned(t *testing.T) {
	server, peer := net.Pipe()
	defer peer.Close()
	defer server.Close()
	raw := &failingNotifyConn{Conn: server}
	raw.fail.Store(true)
	lis := &oneNotifyListener{conn: raw}
	w := &notifyCloseListener{Listener: lis}
	c, err := w.Accept()
	if err != nil {
		t.Fatal(err)
	}
	if c.Close() == nil {
		t.Fatal("fixture close must fail")
	}
	if w.Close() == nil {
		t.Fatal("listener forgot its failed client close")
	}
	raw.fail.Store(false)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	calls := raw.calls.Load()
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if raw.calls.Load() != calls {
		t.Fatal("successful client close was repeated")
	}
	if lis.calls.Load() != 1 {
		t.Fatal("successful listener close was repeated")
	}
}

func TestNotificationClientFailureRetainsManagerStop(t *testing.T) {
	const name = "client-cleanup.service"
	m := managerWith(t, &fakeLauncher{}, map[string]string{name: "[Service]\nExecStart=C:\\Tools\\worker.exe\n"})
	if _, err := m.Start(context.Background(), name); err != nil {
		t.Fatal(err)
	}
	server, peer := net.Pipe()
	defer server.Close()
	defer peer.Close()
	raw := &failingNotifyConn{Conn: server}
	raw.fail.Store(true)
	lis := &notifyCloseListener{Listener: &oneNotifyListener{conn: raw}}
	if _, err := lis.Accept(); err != nil {
		t.Fatal(err)
	}
	rt := &notifyRuntime{lis: lis, done: make(chan struct{})}
	m.mu.Lock()
	m.units[name].notify = rt
	m.mu.Unlock()
	if _, err := m.stopUnit(name); err == nil {
		t.Fatal("failed client close reported stop success")
	}
	m.mu.Lock()
	retained := m.units[name].notify == rt && m.units[name].stopUncertain
	m.mu.Unlock()
	if !retained {
		t.Fatal("failed client cleanup lost manager ownership")
	}
	if _, err := m.Start(context.Background(), name); err == nil {
		t.Fatal("replacement admitted during failed client cleanup")
	}
	raw.fail.Store(false)
	if _, err := m.stopUnit(name); err != nil {
		t.Fatal(err)
	}
}

type lateNotifyListener struct {
	oneNotifyListener
	entered chan struct{}
	release chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func (l *lateNotifyListener) Accept() (notify.Conn, error) {
	close(l.entered)
	<-l.release
	return l.conn, nil
}
func (l *lateNotifyListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func TestNotificationCloseOwnsLateAcceptedClient(t *testing.T) {
	server, peer := net.Pipe()
	defer server.Close()
	defer peer.Close()
	raw := &failingNotifyConn{Conn: server}
	raw.fail.Store(true)
	lis := &lateNotifyListener{oneNotifyListener: oneNotifyListener{conn: raw}, entered: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	w := &notifyCloseListener{Listener: lis}
	accepted := make(chan struct{})
	go func() { _, _ = w.Accept(); close(accepted) }()
	<-lis.entered
	result := make(chan error, 1)
	go func() { result <- w.Close() }()
	<-lis.closed
	select {
	case <-result:
		close(lis.release)
		t.Fatal("close did not wait for the in-flight accept")
	case <-time.After(20 * time.Millisecond):
	}
	close(lis.release)
	<-accepted
	if err := <-result; err == nil {
		t.Fatal("late client's failed close was lost")
	}
	raw.fail.Store(false)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatal("closed listener admitted new accept")
	}
}

func TestNotificationJoinedCloseFailureIsNotSuppressed(t *testing.T) {
	failure := errors.New("native cleanup failed")
	if onlyNetClosed(errors.Join(net.ErrClosed, failure)) {
		t.Fatal("joined failure hidden")
	}
	if !onlyNetClosed(errors.Join(net.ErrClosed, net.ErrClosed)) {
		t.Fatal("already-closed leaves rejected")
	}
}
