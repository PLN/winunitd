//go:build windows

package manager

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/protocol"
)

// TCP loopback is an isolated transport fixture, not a supported daemon endpoint.
// This qualifies default connection budgets and real process quiescence; pipe
// identity and DACL checks retain their independent Windows security tests.
func TestWindowsMaintenanceSurvivesFullControlConnections(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "units"), 0755); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	writeUnit(t, filepath.Join(dir, "units"), "owned.service", fmt.Sprintf("[Service]\nExecStart=%s\nExecStartArg=%ssleep\nTimeoutStopSec=10s\n", exe, winunitdHelperArgPrefix))
	m, err := New(Config{BaseDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(context.Background(), "owned.service"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	owned := m.units["owned.service"].proc
	m.mu.Unlock()
	if owned == nil || !owned.Alive() {
		t.Fatal("native workload did not start")
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		lis.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 2)
	control := &Control{Units: m}
	accepted := make(chan struct{}, 128)
	auth := func(conn net.Conn) (protocol.Peer, error) {
		p, e := protocol.AllowAdmin(conn)
		accepted <- struct{}{}
		return p, e
	}
	go func() { done <- protocol.Serve(ctx, lis, control, auth) }()
	go func() { done <- protocol.ServeMaintenance(ctx, reserved, control, protocol.AllowAdmin) }()
	var clients []net.Conn
	t.Cleanup(func() {
		for _, c := range clients {
			c.Close()
		}
		cancel()
		<-done
		<-done
	})
	for i := 0; i < 128; i++ {
		conn, err := net.DialTimeout("tcp", lis.Addr().String(), 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		clients = append(clients, conn)
		select {
		case <-accepted:
		case <-time.After(5 * time.Second):
			t.Fatal("connection was not admitted", i)
		}
	}
	// Every admitted idle decoder retains a slot; another control connection
	// must close rather than allocate an unbounded server worker.
	overflow, err := net.DialTimeout("tcp", lis.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := overflow.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		overflow.Close()
		t.Fatal(err)
	}
	err = protocol.NewClient(overflow).Call(ctx, protocol.MethodStatus, nil, nil)
	overflow.Close()
	if err == nil {
		t.Fatal("ordinary connection cap was not enforced")
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		t.Fatal("overflow connection was left waiting instead of closed")
	}
	select {
	case <-accepted:
		t.Fatal("overflow connection reached authorization")
	default:
	}
	conn, err := net.DialTimeout("tcp", reserved.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	result, err := protocol.NewClient(conn).Maintenance(ctx, protocol.MaintenanceParams{TimeoutMS: 10000})
	if err != nil || result.State != "quiesced" {
		t.Fatalf("reserved maintenance: %+v %v", result, err)
	}
	if owned.Alive() {
		t.Fatal("maintenance returned with live workload")
	}
	snapshot, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	u := snapshotUnit(t, snapshot, "owned.service")
	if snapshot.Machine.State != "closing" || u.MainPID != 0 || len(u.PendingCleanup) != 0 || len(snapshot.Operations) != 0 {
		t.Fatalf("maintenance discarded pending ownership: %+v", snapshot)
	}
	if _, err := m.Start(ctx, "owned.service"); err == nil {
		t.Fatal("maintenance admitted a replacement")
	}
	t.Logf("idleConnections=128 overflowClosed=true nativeWorkloadExited=true maintenance=%s", time.Since(started))
}
