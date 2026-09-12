package journal

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAggregateCapturePressureBoundsAndRecovers(t *testing.T) {
	s := testStore(t)
	blocked, release := make(chan struct{}), make(chan struct{})
	var opened, released sync.Once
	unblock := func() { released.Do(func() { close(release) }) }
	defer unblock()
	s.onOpen = func() { opened.Do(func() { close(blocked); <-release }) }
	groups := make([]*captureGroup, 5)
	message := strings.Repeat("x", 64<<10)
	for i := range groups {
		groups[i] = &captureGroup{}
		for j := 0; j < 80; j++ {
			s.enqueue(Entry{Unit: fmt.Sprintf("noisy-%d.service", i), Message: message}, groups[i])
		}
	}
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("writer did not stall")
	}
	s.queueMu.Lock()
	queued := s.queuedBytes
	s.queueMu.Unlock()
	if queued != captureQueueBytes {
		t.Fatalf("aggregate queue bytes=%d, want %d", queued, captureQueueBytes)
	}
	for _, group := range groups {
		if group.bytes.Load() > invocationQueueBytes {
			t.Fatal("invocation escaped its byte cap")
		}
	}
	quiet := &captureGroup{}
	lossBefore := uint64(0)
	for i := range groups {
		lossBefore += s.CaptureStats(fmt.Sprintf("noisy-%d.service", i)).DroppedBytes
	}
	s.enqueue(Entry{Unit: "quiet.service", Message: "during pressure"}, quiet)
	// A quiet invocation may displace a newer record from a larger queue.
	if stats := s.CaptureStats("quiet.service"); stats.DroppedRecords != 0 || quiet.records.Load() != 1 {
		t.Fatalf("aggregate pressure crowded out quiet output: %+v", stats)
	}
	lossAfter := uint64(0)
	for i := range groups {
		lossAfter += s.CaptureStats(fmt.Sprintf("noisy-%d.service", i)).DroppedBytes
	}
	if lossAfter-lossBefore != uint64(len(message)) {
		t.Fatal("displaced record loss was not charged to its producer")
	}
	unblock()
	for _, group := range groups {
		group.wg.Wait()
	}
	s.enqueue(Entry{Unit: "quiet.service", Message: "after pressure"}, quiet)
	quiet.wg.Wait()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := s.Read("quiet.service")
	if err != nil || len(entries) != 2 || entries[0].Message != "during pressure" || entries[1].Message != "after pressure" {
		t.Fatalf("post-pressure recovery: entries=%d error=%v", len(entries), err)
	}
	s.queueMu.Lock()
	queued = s.queuedBytes
	s.queueMu.Unlock()
	if queued != 0 {
		t.Fatal("completed pressure retained queue bytes")
	}
}
