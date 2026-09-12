package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestMaintenanceEndpointRestrictsMethodsAndIdentity(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var admin atomic.Bool
	var calls atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- ServeMaintenance(ctx, lis, HandlerFunc(func(context.Context, string, json.RawMessage) (any, error) {
			calls.Add(1)
			return MaintenanceResult{State: "quiesced"}, nil
		}), func(net.Conn) (Peer, error) { return Peer{Owner: true, Administrator: admin.Load()}, nil })
	}()
	t.Cleanup(func() { cancel(); <-done })
	call := func(method string) error {
		conn, err := net.Dial("tcp", lis.Addr().String())
		if err != nil {
			return err
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		return NewClient(conn).Call(ctx, method, nil, nil)
	}
	var rpcErr *Error
	if err := call(MethodMaintenance); !errors.As(err, &rpcErr) || rpcErr.Code != CodePermissionDenied {
		t.Fatalf("owner entered maintenance: %v", err)
	}
	admin.Store(true)
	for _, method := range []string{MethodStart, MethodStatus, MethodStop} {
		if err := call(method); !errors.As(err, &rpcErr) || rpcErr.Code != CodeMethodNotFound {
			t.Fatalf("ordinary method %s entered reserved endpoint: %v", method, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("rejected request reached handler")
	}
	if err := call(MethodMaintenance); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("maintenance was not dispatched")
	}
}
