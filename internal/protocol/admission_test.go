package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"
)

func TestServerReservedAdmission(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan string, 8)
	release := make(chan struct{})
	h := HandlerFunc(func(ctx context.Context, method string, _ json.RawMessage) (any, error) {
		entered <- method
		if method != MethodStop && method != MethodCancelOperation {
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return nil, nil
	})
	done := make(chan error, 1)
	go func() {
		done <- ServeWithLimits(ctx, lis, h, AllowAdmin, ServerLimits{Connections: 8, Requests: 1, Stops: 1, Diagnostics: 1})
	}()
	t.Cleanup(func() { cancel(); <-done })
	call := func(method string) error {
		conn, err := net.Dial("tcp", lis.Addr().String())
		if err != nil {
			return err
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		return NewClient(conn).Call(ctx, method, nil, nil)
	}
	waitEntered := func(want string) {
		t.Helper()
		select {
		case got := <-entered:
			if got != want {
				t.Fatalf("entered %s, want %s", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("handler did not enter")
		}
	}
	startDone := make(chan error, 1)
	go func() { startDone <- call(MethodStart) }()
	waitEntered(MethodStart)
	assertBusy := func(method string) {
		t.Helper()
		var rpcErr *Error
		if err := call(method); !errors.As(err, &rpcErr) || rpcErr.Code != CodeBusy {
			t.Fatalf("%s error = %v, want busy", method, err)
		}
		select {
		case got := <-entered:
			t.Fatalf("rejected request entered handler: %s", got)
		default:
		}
	}
	assertBusy(MethodRestart)
	statusDone := make(chan error, 1)
	go func() { statusDone <- call(MethodStatus) }()
	waitEntered(MethodStatus)
	assertBusy(MethodOperation)
	if err := call(MethodStop); err != nil {
		t.Fatal(err)
	}
	waitEntered(MethodStop)
	if err := call(MethodCancelOperation); err != nil {
		t.Fatal(err)
	}
	waitEntered(MethodCancelOperation)
	close(release)
	if err := <-startDone; err != nil {
		t.Fatal(err)
	}
	if err := <-statusDone; err != nil {
		t.Fatal(err)
	}
	if err := call(MethodStart); err != nil {
		t.Fatalf("released slot: %v", err)
	}
	waitEntered(MethodStart)
}

func TestServerConnectionDeadlines(t *testing.T) {
	for _, sendRequest := range []bool{false, true} {
		t.Run(map[bool]string{false: "idle read", true: "stalled response"}[sendRequest], func(t *testing.T) {
			server, client := net.Pipe()
			defer client.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer server.Close()
				serveConnWithTimeouts(context.Background(), server, HandlerFunc(func(context.Context, string, json.RawMessage) (any, error) { return nil, nil }), AllowAdmin, 20*time.Millisecond, 20*time.Millisecond)
			}()
			if sendRequest {
				_ = client.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if err := encodeMessage(client, Request{Protocol: Name, Version: Version, ID: 1, Method: MethodStatus}); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("connection deadline did not release worker")
			}
		})
	}
}

func TestServerReadDeadlineDoesNotExpireHandler(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		serveConnWithTimeouts(ctx, server, HandlerFunc(func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
			time.Sleep(50 * time.Millisecond)
			return nil, ctx.Err()
		}), AllowAdmin, 20*time.Millisecond, time.Second)
	}()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	if err := NewClient(client).Call(ctx, MethodStart, nil, nil); err != nil {
		t.Fatal(err)
	}
	cancel()
	<-done
}

func TestServerLimitsValidation(t *testing.T) {
	if _, err := (ServerLimits{}).defaults(); err != nil {
		t.Fatal(err)
	}
	for _, limits := range []ServerLimits{
		{Connections: -1}, {Requests: -1}, {Stops: -1}, {Diagnostics: -1}, {ReadTimeout: -1}, {WriteTimeout: -1},
		{Connections: 3, Requests: 1, Stops: 1, Diagnostics: 1},
		{Requests: int(^uint(0) >> 1)},
	} {
		if _, err := limits.defaults(); err == nil {
			t.Fatalf("accepted invalid limits: %+v", limits)
		}
	}
}

func TestServerHardConnectionCap(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{}, 5)
	auth := func(net.Conn) (Peer, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return Peer{}, ctx.Err()
	}
	done := make(chan error, 1)
	go func() {
		done <- ServeWithLimits(ctx, lis, HandlerFunc(func(context.Context, string, json.RawMessage) (any, error) { return nil, nil }), auth, ServerLimits{Connections: 4, Requests: 1, Stops: 1, Diagnostics: 1})
	}()
	t.Cleanup(func() { cancel(); <-done })
	for i := 0; i < 4; i++ {
		conn, err := net.Dial("tcp", lis.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("connection not admitted")
		}
	}
	conn, err := net.Dial("tcp", lis.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var b [1]byte
	_, err = conn.Read(b[:])
	if !isDisconnect(err) {
		t.Fatalf("excess connection was not closed: %v", err)
	}
	select {
	case <-entered:
		t.Fatal("excess connection entered authorizer")
	default:
	}
}

func TestAdmissionDoesNotOverrideAuthorization(t *testing.T) {
	h := &admittedHandler{requests: make(chan struct{}, 1)}
	h.requests <- struct{}{}
	resp := dispatch(context.Background(), h, Peer{}, &Request{Protocol: Name, Version: Version, ID: 1, Method: MethodStart})
	if resp.Error == nil || resp.Error.Code != CodePermissionDenied {
		t.Fatalf("response: %+v", resp)
	}
}
