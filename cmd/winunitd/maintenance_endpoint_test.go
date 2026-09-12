package main

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/manager"
	"github.com/PLN/winunitd/internal/protocol"
)

func TestControlListenerFailureClosesBothEndpoints(t *testing.T) {
	for _, failed := range []string{"control", "maintenance"} {
		t.Run(failed, func(t *testing.T) {
			control, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer control.Close()
			maintenance, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer maintenance.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ready := make(chan struct{})
			done := make(chan error, 1)
			h := protocol.HandlerFunc(func(context.Context, string, json.RawMessage) (any, error) { return nil, nil })
			go func() {
				done <- serveControlEndpoints(ctx, control, maintenance, h, protocol.AllowAdmin, func() { close(ready) })
			}()
			select {
			case <-ready:
			case <-time.After(3 * time.Second):
				t.Fatal("endpoints not ready")
			}
			if failed == "control" {
				_ = control.Close()
			} else {
				_ = maintenance.Close()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("listener failure was hidden")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("sibling listener did not stop")
			}
			for _, lis := range []net.Listener{control, maintenance} {
				conn, err := net.DialTimeout("tcp", lis.Addr().String(), time.Second)
				if err == nil {
					conn.Close()
					t.Fatal("listener remained available after sibling failure")
				}
			}
		})
	}
}

func TestMaintenanceSurvivesOrdinaryConnectionSaturation(t *testing.T) {
	control, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	maintenance, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer maintenance.Close()
	m, err := manager.New(manager.Config{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	accepted := make(chan struct{}, 128)
	auth := func(conn net.Conn) (protocol.Peer, error) {
		if conn.LocalAddr().String() == control.Addr().String() {
			accepted <- struct{}{}
		}
		return protocol.Peer{Administrator: true}, nil
	}
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- serveControlEndpoints(ctx, control, maintenance, &manager.Control{Units: m}, auth, func() { close(ready) })
	}()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("endpoints did not become ready")
	}
	for i := 0; i < 128; i++ {
		conn, err := net.Dial("tcp", control.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		select {
		case <-accepted:
		case <-ctx.Done():
			t.Fatal("normal connections did not reach saturation")
		}
	}
	// The ordinary transport is full before any request bytes are sent.
	conn, err := net.Dial("tcp", maintenance.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	result, err := protocol.NewClient(conn).Maintenance(ctx, protocol.MaintenanceParams{TimeoutMS: 1000})
	if err != nil || result.State != "quiesced" {
		t.Fatalf("maintenance starved behind raw control connections: %+v %v", result, err)
	}
	if _, err := m.Reload(); err == nil {
		t.Fatal("maintenance did not close normal work admission")
	}
}
