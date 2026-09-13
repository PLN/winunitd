package notify

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestNotificationConnectionAdmission(t *testing.T) {
	lis, err := ListenTCP()
	if err != nil {
		t.Fatal(err)
	}
	checkNotificationAdmission(t, lis, 0)
}

func checkNotificationAdmission(t *testing.T, lis Listener, clientPID int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	done := make(chan struct{})
	got := make(chan Message, 4)
	var authorized atomic.Int32
	var deny atomic.Bool
	deny.Store(true)
	go func() {
		defer close(done)
		ServeAccept(ctx, lis, func(pid int) bool {
			if deny.Load() || pid != clientPID {
				return false
			}
			authorized.Add(1)
			return true
		}, func(m Message) { got <- m })
	}()
	var clients []net.Conn
	defer func() {
		cancel()
		_ = lis.Close()
		for _, c := range clients {
			_ = c.Close()
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("notification readers were not joined")
		}
	}()
	// Rejected clients must release admission. More than a full budget of
	// denials must not prevent the subsequent authorized workload.
	for i := 0; i < 65; i++ {
		c, err := Dial(ctx, lis.Addr())
		if err != nil {
			t.Fatal(err)
		}
		_ = c.SetDeadline(time.Now().Add(time.Second))
		var b [1]byte
		_, err = c.Read(b[:])
		_ = c.Close()
		if err == nil {
			t.Fatal("denied client received acceptance")
		}
		if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
			t.Fatal("denied client retained admission")
		}
	}
	deny.Store(false)
	for i := 0; i < 64; i++ {
		c, err := Dial(ctx, lis.Addr())
		if err != nil {
			t.Fatal(err)
		}
		clients = append(clients, c)
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		banner := make([]byte, len(acceptanceBanner))
		if _, err := io.ReadFull(c, banner); err != nil || string(banner) != acceptanceBanner {
			t.Fatalf("connection %d acceptance: %q %v", i, banner, err)
		}
		_ = c.SetDeadline(time.Time{})
	}
	busy, err := Dial(ctx, lis.Addr())
	if err != nil {
		t.Fatal(err)
	}
	clients = append(clients, busy)
	_ = busy.SetDeadline(time.Now().Add(time.Second))
	var b [1]byte
	_, err = busy.Read(b[:])
	if err == nil {
		t.Fatal("65th notification client was admitted")
	}
	if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("overload did not close the connection promptly")
	}
	if authorized.Load() != 64 {
		t.Fatal("overload performed authorization instead of rejecting admission")
	}
	_ = clients[0].Close()
	if err := SendRetry(ctx, lis.Addr(), Message{Ready: true}); err != nil {
		t.Fatal("notification did not recover after capacity release:", err)
	}
	select {
	case m := <-got:
		if !m.Ready {
			t.Fatal("recovered notification lost READY")
		}
	case <-ctx.Done():
		t.Fatal("recovered notification was not delivered")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("saturated notification shutdown did not join readers")
	}
}
