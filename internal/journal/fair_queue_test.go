package journal

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestQuietCaptureWritesBeforeNoisyQueueDrains(t *testing.T) {
	s := testStore(t)
	noisy, quiet := &captureGroup{}, &captureGroup{}
	blocked, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var opened atomic.Int32
	quietOpened := make(chan int64, 1)
	s.onOpen = func() {
		switch opened.Add(1) {
		case 1:
			close(blocked)
			<-release
		case 2:
			quietOpened <- noisy.records.Load()
		}
	}
	s.enqueue(Entry{Unit: "noisy.service", Message: "first"}, noisy)
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("storage did not stall")
	}
	for i := 0; i < 1000; i++ {
		s.enqueue(Entry{Unit: "noisy.service", Message: fmt.Sprint(i)}, noisy)
	}
	s.enqueue(Entry{Unit: "quiet.service", Message: "quiet"}, quiet)
	unblock()
	select {
	case remaining := <-quietOpened:
		if remaining < 999 {
			t.Fatalf("quiet output waited behind %d noisy records", 1000-remaining)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("quiet writer did not make progress")
	}
	noisy.wg.Wait()
	quiet.wg.Wait()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := s.Read("noisy.service")
	if err != nil || len(entries) != 1001 {
		t.Fatalf("noisy entries: %d, %v", len(entries), err)
	}
	for i, e := range entries[1:] {
		if e.Message != fmt.Sprint(i) {
			t.Fatal("round robin reordered one invocation")
		}
	}
}

func TestCaptureGroupAdmissionIsBounded(t *testing.T) {
	s := testStore(t)
	blocked, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	s.onOpen = func() {
		select {
		case <-blocked:
		default:
			close(blocked)
			<-release
		}
	}
	s.enqueue(Entry{Unit: "writer.service", Message: "inflight"}, &captureGroup{})
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("writer did not stall")
	}
	for i := 0; i < maxQueuedCaptureGroups; i++ {
		s.enqueue(Entry{Unit: "many.service", Message: "x"}, &captureGroup{})
	}
	s.enqueue(Entry{Unit: "overflow.service", Message: "overflow"}, &captureGroup{})
	if s.CaptureStats("overflow.service").DroppedRecords != 1 {
		t.Fatal("group admission did not reject overflow")
	}
	s.queueMu.Lock()
	count := len(s.captureQueues)
	s.queueMu.Unlock()
	if count != maxQueuedCaptureGroups {
		t.Fatalf("queued groups: %d", count)
	}
	unblock()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	if len(s.captureQueues) != 0 || s.captureOrder.Len() != 0 || s.queuedRecords != 0 || s.queuedBytes != 0 {
		t.Fatal("drained queue retained accounting")
	}
}
