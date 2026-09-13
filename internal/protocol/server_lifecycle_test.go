package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

type delayedCloseListener struct {
	net.Listener
	entered   chan struct{}
	release   chan struct{}
	acceptErr error
}

func (l *delayedCloseListener) Accept() (net.Conn, error) { return nil, l.acceptErr }
func (l *delayedCloseListener) Close() error {
	close(l.entered)
	<-l.release
	return l.Listener.Close()
}

func TestServeJoinsListenerCloseAfterAcceptFailure(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	wrapped := &delayedCloseListener{Listener: lis, entered: make(chan struct{}), release: make(chan struct{}), acceptErr: errors.New("injected accept failure")}
	var release sync.Once
	unblock := func() { release.Do(func() { close(wrapped.release) }) }
	defer unblock()
	done := make(chan error, 1)
	go func() {
		done <- Serve(context.Background(), wrapped, HandlerFunc(func(context.Context, string, json.RawMessage) (any, error) { return nil, nil }), AllowAdmin)
	}()
	select {
	case <-wrapped.entered:
	case <-time.After(time.Second):
		t.Fatal("listener close was not requested")
	}
	select {
	case <-done:
		t.Fatal("Serve returned before listener close completed")
	case <-time.After(20 * time.Millisecond):
	}
	unblock()
	select {
	case err := <-done:
		if !errors.Is(err, wrapped.acceptErr) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not join completed listener close")
	}
}

type failingControlListener struct {
	net.Listener
	fail     <-chan struct{}
	err      error
	accepted bool
}

func (l *failingControlListener) Accept() (net.Conn, error) {
	if !l.accepted {
		l.accepted = true
		return l.Listener.Accept()
	}
	<-l.fail
	return nil, l.err
}

func TestServeFailureCancelsAcceptedHandler(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fail := make(chan struct{})
	acceptErr := errors.New("injected accept failure")
	entered, cancelled := make(chan struct{}), make(chan struct{})
	h := HandlerFunc(func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	})
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, &failingControlListener{Listener: lis, fail: fail, err: acceptErr}, h, AllowAdmin)
	}()
	conn, err := net.DialTimeout("tcp", lis.Addr().String(), time.Second)
	if err != nil {
		close(fail)
		t.Fatal(err)
	}
	defer conn.Close()
	client := NewClient(conn)
	callDone := make(chan error, 1)
	go func() { _, err := client.ListUnits(ctx); callDone <- err }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		close(fail)
		t.Fatal("request not admitted")
	}
	close(fail)
	select {
	case err := <-done:
		if !errors.Is(err, acceptErr) {
			t.Fatalf("Serve error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("accepted handler survived server failure")
	}
	select {
	case err := <-callDone:
		if err == nil {
			t.Fatal("interrupted request succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("accepted connection remained open")
	}
	if ctx.Err() != nil {
		t.Fatal("server cancelled caller context")
	}
}
