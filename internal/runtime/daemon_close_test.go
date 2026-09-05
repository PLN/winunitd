package runtime

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDaemonCloseContextJoinsPendingCall(t *testing.T) {
	j, err := OpenDaemonJob()
	if err != nil {
		t.Fatal(err)
	}
	// Hold the adapter lock to model a pending native close without relying
	// on a real OS call to stall indefinitely.
	j.mu.Lock()
	locked := true
	defer func() {
		if locked {
			j.mu.Unlock()
		}
		_ = j.Close()
	}()
	var first *daemonCloseAttempt
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		err := j.CloseContext(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("close result: %v", err)
		}
		j.closeWait.mu.Lock()
		pending := j.closeWait.pending
		j.closeWait.mu.Unlock()
		if pending == nil {
			t.Fatal("deadline discarded pending close")
		}
		if first == nil {
			first = pending
		} else if pending != first {
			t.Fatal("retry started another close")
		}
	}
	j.mu.Unlock()
	locked = false
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := j.CloseContext(ctx); err != nil {
		t.Fatal("close retry", err)
	}
	if !j.Closed() {
		t.Fatal("retry did not close job")
	}
}
