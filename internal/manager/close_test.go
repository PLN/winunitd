package manager

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type controlledCloseWatch struct {
	calls   atomic.Int32
	fail    atomic.Bool
	release <-chan struct{}
}

func (w *controlledCloseWatch) C() <-chan struct{} { return nil }
func (w *controlledCloseWatch) Close() error {
	w.calls.Add(1)
	if w.release != nil {
		<-w.release
	}
	if w.fail.Load() {
		return errors.New("injected watch close failure")
	}
	return nil
}

func TestManagerCloseDeadlineJoinsPendingWatch(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	w := &controlledCloseWatch{release: release}
	m := &Manager{units: map[string]*unitRuntime{"pending.path": {hub: &watchRuntime{watches: []watchIO{w}}}}}
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		err := m.CloseContext(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("close result: %v", err)
		}
	}
	if w.calls.Load() != 1 {
		t.Fatal("close retry duplicated a blocked watcher close")
	}
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if !closed {
		t.Fatal("deadline reopened manager admission")
	}
	unblock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.CloseContext(ctx); err != nil {
		t.Fatal("close retry", err)
	}
	if w.calls.Load() != 1 {
		t.Fatal("completed watch was closed again")
	}
}

func TestManagerCloseRetainsFailedWatch(t *testing.T) {
	w := &controlledCloseWatch{}
	w.fail.Store(true)
	m := &Manager{units: map[string]*unitRuntime{"failed.path": {hub: &watchRuntime{watches: []watchIO{w}}}}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.CloseContext(ctx); err == nil {
		t.Fatal("failed watch close reported success")
	}
	m.mu.Lock()
	pending := len(m.closePending)
	m.mu.Unlock()
	if pending != 1 {
		t.Fatal("failed watch close lost ownership")
	}
	w.fail.Store(false)
	if err := m.CloseContext(ctx); err != nil {
		t.Fatal("close retry", err)
	}
	m.mu.Lock()
	pending = len(m.closePending)
	m.mu.Unlock()
	if pending != 0 || w.calls.Load() != 2 {
		t.Fatal("failed close retry did not finish")
	}
}
