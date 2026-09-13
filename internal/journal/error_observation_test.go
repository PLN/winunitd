package journal

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type delayedStorageError struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (e *delayedStorageError) Error() string {
	e.once.Do(func() { close(e.entered) })
	<-e.release
	return "injected retirement error"
}

func TestStorageErrorObservationPreservesQueueProgress(t *testing.T) {
	s := testStore(t)
	e := &delayedStorageError{entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(e.release) }) }
	defer release()
	var failed atomic.Bool
	s.onClose = func() error {
		if failed.CompareAndSwap(false, true) {
			return e
		}
		return nil
	}
	capture := s.Attach("work.service", 42, "work", strings.NewReader("before failure\n"), nil)
	done := make(chan bool, 1)
	go func() { done <- capture.WaitContext(context.Background()) }()
	select {
	case <-e.entered:
	case <-time.After(time.Second):
		t.Fatal("retirement error observation did not enter")
	}
	progress := make(chan bool, 1)
	go func() {
		if err := s.RetainNames([]string{"work.service", "other.service"}); err != nil {
			progress <- false
			return
		}
		s.RecordDiagnostic("other.service", "other", "rejected transition")
		_ = s.TotalCaptureStats()
		other := s.AttachConcurrent("other.service", 43, "other", strings.NewReader("independent output\n"), nil)
		progress <- other.WaitContext(context.Background())
	}()
	select {
	case ok := <-progress:
		if !ok {
			t.Fatal("independent capture failed")
		}
	case <-time.After(time.Second):
		t.Fatal("storage error formatting blocked queue admission and independent capture")
	}
	release()
	if <-done {
		t.Fatal("failed retirement reported successful capture")
	}
	stats := s.CaptureStats("work.service")
	if stats.StorageErrors != 1 || stats.LastStorageError != "injected retirement error" {
		t.Fatalf("storage error observation lost: %+v", stats)
	}
	if !capture.WaitContext(context.Background()) {
		t.Fatal("retained capture did not recover")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := s.Read("other.service")
	if err != nil || len(entries) != 2 {
		t.Fatalf("independent output/diagnostic not retained: %d %v", len(entries), err)
	}
}
