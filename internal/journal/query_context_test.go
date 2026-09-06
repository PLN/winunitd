package journal

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestQueryCancellationRetainsBoundedScanWorkers(t *testing.T) {
	s := testStore(t)
	const name = "query.service"
	if err := s.append(Entry{Unit: name, Message: "before"}); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, queryWorkers)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	s.onScan = func() { entered <- struct{}{}; <-release }
	for i := 0; i < queryWorkers; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan error, 1)
		go func() { _, _, _, err := s.QueryPageContext(ctx, name, time.Time{}, "original", 1024); finished <- err }()
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			cancel()
			t.Fatal("scan did not start")
		}
		// Cancel only after admission so scheduling delay cannot skip the scan.
		cancel()
		select {
		case err := <-finished:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("query cancellation: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("canceled query did not return while its scan was blocked")
		}
	}
	if _, cursor, _, err := s.QueryPageContext(context.Background(), name, time.Time{}, "original", 1024); !errors.Is(err, ErrQueryBusy) || cursor != "original" {
		t.Fatalf("overload was not rejected with unchanged cursor: %q %v", cursor, err)
	}
	if len(s.querySlots) != queryWorkers {
		t.Fatal("cancellation released a still-blocked scan slot")
	}
	// Scans do not own the write lock; output can continue while reads stall.
	if err := s.append(Entry{Unit: name, Message: "after"}); err != nil {
		t.Fatal(err)
	}
	unblock()
	deadline := time.Now().Add(time.Second)
	for len(s.querySlots) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("completed scans retained admission slots")
		}
		time.Sleep(time.Millisecond)
	}
	s.onScan = nil
	entries, _, _, err := s.QueryPageContext(context.Background(), name, time.Time{}, "", 1024)
	if err != nil || len(entries) != 2 {
		t.Fatalf("query recovery: entries=%d error=%v", len(entries), err)
	}
}

func TestQueryDeadlineWhileWaitingForFlushLock(t *testing.T) {
	s := testStore(t)
	const name = "locked-query.service"
	if err := s.append(Entry{Unit: name, Message: "pending"}); err != nil {
		t.Fatal(err)
	}
	f := s.fileExisting(name)
	f.mu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	_, cursor, _, err := s.QueryPageContext(ctx, name, time.Time{}, "original", 1024)
	cancel()
	f.mu.Unlock()
	if !errors.Is(err, context.DeadlineExceeded) || cursor != "original" {
		t.Fatalf("blocked flush deadline: %q %v", cursor, err)
	}
	deadline := time.Now().Add(time.Second)
	for len(s.querySlots) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("flush waiter did not leave after cancellation")
		}
		time.Sleep(time.Millisecond)
	}
}
