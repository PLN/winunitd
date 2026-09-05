package journal

import (
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBlockedStorageDoesNotBlockCaptureOrDeadline(t *testing.T) {
	s := testStore(t)
	blocked := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	s.onOpen = func() { close(blocked); <-release }
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()
	s.Attach("worker.service", 42, "example-invocation", r, nil)
	written := make(chan error, 1)
	go func() {
		_, err := io.WriteString(w, "first\n"+strings.Repeat("x", invocationQueueBytes+MaxCaptureFragment*4))
		_ = w.Close()
		written <- err
	}()
	select {
	case <-blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not reach storage")
	}
	select {
	case err := <-written:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocked storage stopped pipe draining")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if s.WaitContext(ctx, "worker.service") {
		t.Fatal("wait reported persistence while storage was blocked")
	}
	stats := s.CaptureStats("worker.service")
	if stats.DroppedRecords == 0 || stats.DroppedBytes == 0 {
		t.Fatal("overflow was not reported")
	}
	s.queueMu.Lock()
	bytes := s.queuedBytes
	s.queueMu.Unlock()
	if bytes > invocationQueueBytes {
		t.Fatalf("pending message bytes = %d", bytes)
	}
	ctxClose, cancelClose := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelClose()
	if err := s.CloseContext(ctxClose); err == nil {
		t.Fatal("close reported success with blocked writer")
	}
	unblock()
	if err := s.Close(); err != nil {
		t.Fatal("close retry failed", err)
	}
}

func TestStorageWriteFailureIsReported(t *testing.T) {
	s := testStore(t)
	if err := os.Mkdir(s.path("worker.service"), 0700); err != nil {
		t.Fatal(err)
	}
	s.Attach("worker.service", 42, "example-invocation", strings.NewReader("lost\n"), nil)
	s.Wait("worker.service")
	stats := s.CaptureStats("worker.service")
	if stats.DroppedRecords != 1 || stats.DroppedBytes != 4 || stats.StorageErrors != 1 || stats.LastStorageError == "" {
		t.Fatalf("failure counters: records=%d bytes=%d errors=%d", stats.DroppedRecords, stats.DroppedBytes, stats.StorageErrors)
	}
}

func TestSyncStallHonorsWaitAndCloseDeadlines(t *testing.T) {
	s := testStore(t)
	s.append(Entry{Unit: "worker.service", Message: "ready"})
	blocked := make(chan struct{})
	release := make(chan struct{})
	var entered, released sync.Once
	unblock := func() { released.Do(func() { close(release) }) }
	defer unblock()
	s.onSync = func() { entered.Do(func() { close(blocked) }); <-release }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if s.WaitContext(ctx, "worker.service") {
		t.Fatal("sync stall reported successful wait")
	}
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("sync hook not reached")
	}
	ctxClose, cancelClose := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelClose()
	if err := s.CloseContext(ctxClose); err == nil {
		t.Fatal("sync stall reported successful close")
	}
	unblock()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNoisyInvocationLeavesRecordCapacityForAnotherUnit(t *testing.T) {
	s := testStore(t)
	blocked, release := make(chan struct{}), make(chan struct{})
	var once, opened sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	s.onOpen = func() { opened.Do(func() { close(blocked); <-release }) }
	noisy := &captureGroup{}
	for i := 0; i < captureQueueRecords*2; i++ {
		s.enqueue(Entry{Unit: "noisy.service", Message: "x", InvocationID: "noisy-invocation"}, noisy)
	}
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("writer did not block")
	}
	quiet := &captureGroup{}
	s.enqueue(Entry{Unit: "quiet.service", Message: "important", InvocationID: "quiet-invocation"}, quiet)
	if s.CaptureStats("noisy.service").DroppedRecords == 0 {
		t.Fatal("noisy invocation escaped record budget")
	}
	if s.CaptureStats("quiet.service").DroppedRecords != 0 || quiet.records.Load() != 1 {
		t.Fatal("noisy invocation crowded out another unit")
	}
	if noisy.records.Load() > invocationQueueRecords {
		t.Fatal("invocation exceeded record cap")
	}
	unblock()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := s.Read("quiet.service")
	if err != nil || len(entries) != 1 || entries[0].Message != "important" {
		t.Fatalf("quiet record not preserved: entries=%+v err=%v", entries, err)
	}
}
