package journal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCaptureNameChurnRetiresFiles(t *testing.T) {
	s := testStore(t)
	for i := 0; i < maxUnitFiles*2; i++ {
		name := fmt.Sprintf("churn-%d.service", i)
		capture := s.Attach(name, 42, "churn", strings.NewReader("retained log\n"), nil)
		if !capture.WaitContext(context.Background()) {
			t.Fatal("capture did not persist", i)
		}
		s.mu.Lock()
		count := len(s.files)
		s.mu.Unlock()
		if count != 0 {
			t.Fatalf("completed name %d retained %d file records", i, count)
		}
	}
	entries, err := s.Read("churn-0.service")
	if err != nil || len(entries) != 1 || entries[0].Message != "retained log" {
		t.Fatalf("retired log query: %v, %v", entries, err)
	}
}

func TestRetiredFileReopensWithRepairAndRotation(t *testing.T) {
	s := testStore(t)
	const name = "reopen.service"
	write := func(message string) {
		t.Helper()
		c := s.Attach(name, 42, "reopen", strings.NewReader(message+"\n"), nil)
		if !c.WaitContext(context.Background()) {
			t.Fatal("capture failed")
		}
	}
	write("first")
	f, err := os.OpenFile(s.path(name), os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{interrupted"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	write("second")
	entries, err := s.Read(name)
	if err != nil || len(entries) != 2 || entries[0].Message != "first" || entries[1].Message != "second" {
		t.Fatalf("repaired reopen: %v, %v", entries, err)
	}
	if s.CaptureStats(name).StorageErrors != 1 {
		t.Fatal("repair was not reported")
	}
	s.maxSize = 1 // every following record rotates the already persisted file
	write("third")
	write("fourth")
	entries, err = s.Read(name)
	if err != nil || len(entries) != 4 || entries[3].Message != "fourth" {
		t.Fatalf("rotation after reopen: %v, %v", entries, err)
	}
}

func TestRetirementPreservesFailedStorageAndClose(t *testing.T) {
	for _, failure := range []string{"write", "close"} {
		t.Run(failure, func(t *testing.T) {
			s := testStore(t)
			s.flushEvery = time.Hour
			const name = "retained.service"
			if err := s.append(Entry{Unit: name, Message: "persisted"}); err != nil {
				t.Fatal(err)
			}
			if err := s.syncUnit(name); err != nil {
				t.Fatal(err)
			}
			u := s.fileExisting(name)
			var fail atomic.Bool
			fail.Store(true)
			var w *recoveringWriter
			if failure == "close" {
				s.onClose = func() error {
					if fail.Load() {
						return errors.New("injected close failure")
					}
					return nil
				}
			} else {
				w = &recoveringWriter{file: u.f, remaining: 7}
				w.fail.Store(true)
				u.mu.Lock()
				u.w = &recordBuffer{writer: w}
				u.mu.Unlock()
			}
			if err := s.append(Entry{Unit: name, Message: "pending"}); err != nil {
				t.Fatal(err)
			}
			if s.WaitContext(context.Background(), name) {
				t.Fatal("failed storage released ownership")
			}
			if s.fileExisting(name) != u || u.f == nil || u.w == nil {
				t.Fatal("failed resource was discarded")
			}
			if s.CaptureStats(name).DroppedRecords != 0 {
				t.Fatal("retained record counted as lost")
			}
			fail.Store(false)
			if w != nil {
				w.fail.Store(false)
			}
			if !s.WaitContext(context.Background(), name) {
				t.Fatal("retry failed")
			}
			if s.fileExisting(name) != nil {
				t.Fatal("successful retry retained file")
			}
			entries, err := s.Read(name)
			if err != nil || len(entries) != 2 || entries[1].Message != "pending" {
				t.Fatalf("retry: %v %v", entries, err)
			}
		})
	}
}

func TestRetirementRejectsStaleWriterAndBoundsFileAdmission(t *testing.T) {
	s := testStore(t)
	const name = "selected.service"
	u, err := s.file(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.syncAndRetireUnit(name); err != nil {
		t.Fatal(err)
	}
	if err := u.write([]byte("old\n"), 3); !errors.Is(err, errFileRetired) {
		t.Fatal("stale writer was not rejected", err)
	}
	if err := s.append(Entry{Unit: name, Message: "current"}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < maxUnitFiles; i++ {
		if _, err := s.file(fmt.Sprintf("reserved-%d.service", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.append(Entry{Unit: "overflow.service", Message: "rejected"}); !errors.Is(err, errFileCapacity) {
		t.Fatal("file limit was not enforced", err)
	}
	if !s.WaitContext(context.Background(), name) {
		t.Fatal("could not release capacity")
	}
	if err := s.append(Entry{Unit: "overflow.service", Message: "accepted"}); err != nil {
		t.Fatal(err)
	}
}

func TestLateCaptureCannotReuseEarlierSync(t *testing.T) {
	s := testStore(t)
	s.flushEvery = time.Hour
	const name = "generation.service"
	if err := s.append(Entry{Unit: name, Message: "first"}); err != nil {
		t.Fatal(err)
	}
	synced, release, joined := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once, releaseOnce, joinOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	s.onSynced = func() { once.Do(func() { close(synced); <-release }) }
	s.onSyncJoin = func() { joinOnce.Do(func() { close(joined) }) }
	first := make(chan bool, 1)
	go func() { first <- s.WaitContext(context.Background(), name) }()
	<-synced
	if err := s.append(Entry{Unit: name, Message: "later"}); err != nil {
		t.Fatal(err)
	}
	second := make(chan bool, 1)
	go func() { second <- s.WaitContext(context.Background(), name) }()
	select {
	case <-joined:
	case <-time.After(time.Second):
		t.Fatal("later capture did not join prior sync")
	}
	unblock()
	if !<-first || !<-second {
		t.Fatal("sync failed")
	}
	if s.fileExisting(name) != nil {
		t.Fatal("later writes were omitted from successful wait")
	}
	raw, err := os.ReadFile(s.path(name))
	if err != nil || !strings.Contains(string(raw), "later") {
		t.Fatal("later record not persisted", err)
	}
}

func TestCounterHistoryRetainsLoadedNamesAndLifetimeTotals(t *testing.T) {
	s := testStore(t)
	names := make([]string, MaxRetainedNames)
	for i := range names {
		names[i] = fmt.Sprintf("loaded-%d.service", i)
	}
	if err := s.RetainNames(names); err != nil {
		t.Fatal(err)
	}
	add := func(name string) {
		s.queueMu.Lock()
		s.dropCaptureLocked(captureWrite{entry: Entry{Unit: name, Message: "lost"}})
		s.queueMu.Unlock()
	}
	for _, name := range names {
		add(name)
	}
	for i := 0; i < maxStatsNames*3; i++ {
		add(fmt.Sprintf("removed-%d.service", i))
	}
	if len(s.dropped) != maxStatsNames || s.statsOrder.Len() != maxStatsNames {
		t.Fatal("history grew beyond bound")
	}
	for _, name := range names {
		if s.CaptureStats(name).DroppedRecords != 1 {
			t.Fatal("loaded counter evicted", name)
		}
	}
	if s.CaptureStats("removed-0.service").DroppedRecords != 0 {
		t.Fatal("old history not evicted")
	}
	want := uint64(MaxRetainedNames + maxStatsNames*3)
	if got := s.TotalCaptureStats(); got.DroppedRecords != want || got.DroppedBytes != want*4 {
		t.Fatalf("lifetime counters lost: %+v", got)
	}
	if err := s.RetainNames(append(names, "too-many")); err == nil {
		t.Fatal("unbounded retention accepted")
	}
	if err := s.RetainNames(nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxStatsNames; i++ {
		add(fmt.Sprintf("next-%d.service", i))
	}
	if s.CaptureStats(names[0]).DroppedRecords != 0 {
		t.Fatal("removed loaded name remained pinned")
	}
}

func TestConcurrentRetirementPreservesEveryCapture(t *testing.T) {
	s := testStore(t)
	const name = "concurrent.service"
	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		workers.Go(func() {
			for i := 0; i < 16; i++ {
				message := fmt.Sprintf("%d-%d", worker, i)
				capture := s.AttachConcurrent(name, 42, message, strings.NewReader(message+"\n"), nil)
				if !capture.WaitContext(context.Background()) {
					t.Error("concurrent capture failed", message)
					return
				}
			}
		})
	}
	workers.Wait()
	entries, err := s.Read(name)
	if err != nil || len(entries) != 128 {
		t.Fatalf("concurrent reopen lost records: %d %v", len(entries), err)
	}
	seen := make(map[string]bool)
	for _, entry := range entries {
		if seen[entry.Message] {
			t.Fatal("duplicate concurrent record", entry.Message)
		}
		seen[entry.Message] = true
	}
	if s.fileExisting(name) != nil {
		t.Fatal("completed concurrent captures retained a file")
	}
}
