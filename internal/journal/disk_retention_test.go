package journal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeRetiredJournal(t *testing.T, s *Store, name, message string) {
	t.Helper()
	if err := s.append(Entry{Unit: name, Message: message}); err != nil {
		t.Fatal(err)
	}
	if err := s.syncAndRetireUnit(name); err != nil {
		t.Fatal(err)
	}
}

func assertDiskAccounting(t *testing.T, s *Store) {
	t.Helper()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	count := 0
	for _, entry := range entries {
		if _, owned := retainedDiskName(entry.Name()); !owned {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		total += info.Size()
		count++
	}
	s.diskMu.Lock()
	defer s.diskMu.Unlock()
	if total != s.disk.bytes || count != len(s.disk.files) {
		t.Fatalf("disk bytes/files=%d/%d; accounted=%d/%d", total, count, s.disk.bytes, len(s.disk.files))
	}
}

func TestDiskRetentionBoundsRetiredNameChurn(t *testing.T) {
	s := testStore(t)
	s.maxDiskFiles = 2
	writeRetiredJournal(t, s, "first.service", "first")
	writeRetiredJournal(t, s, "second.service", "second")
	writeRetiredJournal(t, s, "third.service", "third")
	if _, err := os.Stat(s.path("first.service")); !os.IsNotExist(err) {
		t.Fatal("oldest history survived quota", err)
	}
	for _, name := range []string{"second.service", "third.service"} {
		entries, err := s.Read(name)
		if err != nil || len(entries) != 1 {
			t.Fatal("recent history lost", name, err)
		}
	}
	stats := s.TotalCaptureStats()
	if stats.EvictedFiles != 1 || stats.EvictedBytes == 0 || stats.DroppedRecords != 0 || stats.StorageErrors != 0 {
		t.Fatalf("retention counters: %+v", stats)
	}
	if s.CaptureStats("first.service").EvictedFiles != 1 {
		t.Fatal("eviction not attributed to history owner")
	}
	assertDiskAccounting(t, s)
}

func TestDiskRetentionCountsBufferedBytesAndPreservesOpenFiles(t *testing.T) {
	s := testStore(t)
	s.flushEvery = time.Hour
	s.maxDiskSize = 700
	if err := s.append(Entry{Unit: "open.service", Message: strings.Repeat("a", 300)}); err != nil {
		t.Fatal(err)
	}
	if err := s.append(Entry{Unit: "other.service", Message: strings.Repeat("b", 300)}); !errors.Is(err, errDiskCapacity) {
		t.Fatal("buffered bytes bypassed quota", err)
	}
	entries, err := s.Read("open.service")
	if err != nil || len(entries) != 1 {
		t.Fatal("open journal evicted", err)
	}
	if s.TotalCaptureStats().EvictedFiles != 0 {
		t.Fatal("open history evicted")
	}
	if err := s.syncAndRetireUnit("open.service"); err != nil {
		t.Fatal(err)
	}
	if err := s.append(Entry{Unit: "other.service", Message: strings.Repeat("b", 300)}); err != nil {
		t.Fatal("retired history did not release budget", err)
	}
	if err := s.syncAndRetireUnit("other.service"); err != nil {
		t.Fatal(err)
	}
	assertDiskAccounting(t, s)
}

func TestDiskRetentionIndexesExistingHistoryWithoutTouchingUnknownFiles(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	writeRetiredJournal(t, s, "old.service", "retained across restart")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(dir, "unrelated.txt")
	if err := os.WriteFile(unknown, []byte("unowned"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.maxDiskFiles = 1
	writeRetiredJournal(t, s, "new.service", "new")
	if data, err := os.ReadFile(unknown); err != nil || string(data) != "unowned" {
		t.Fatal("unrelated file changed", err)
	}
	if _, err := os.Stat(s.path("old.service")); !os.IsNotExist(err) {
		t.Fatal("preexisting history bypassed quota", err)
	}
	assertDiskAccounting(t, s)
}

func TestDiskRetentionFailurePreservesHistoryAndRetries(t *testing.T) {
	s := testStore(t)
	s.maxDiskFiles = 1
	writeRetiredJournal(t, s, "old.service", "preserved")
	s.onRetentionRemove = func(string) error { return errors.New("injected deletion failure") }
	c := s.Attach("next.service", 42, "next", strings.NewReader("rejected\n"), nil)
	if !c.WaitContext(context.Background()) {
		t.Fatal("empty rejected capture did not settle")
	}
	stats := s.CaptureStats("next.service")
	if stats.DroppedRecords != 1 || stats.StorageErrors == 0 || !strings.Contains(stats.LastStorageError, "injected deletion failure") {
		t.Fatalf("hidden retention failure: %+v", stats)
	}
	if s.TotalCaptureStats().EvictedFiles != 0 {
		t.Fatal("failed deletion reported eviction")
	}
	entries, err := s.Read("old.service")
	if err != nil || len(entries) != 1 {
		t.Fatal("failed removal discarded history", err)
	}
	s.onRetentionRemove = nil
	writeRetiredJournal(t, s, "next.service", "accepted")
	assertDiskAccounting(t, s)
}

func TestBlockedRetentionPreservesIndependentSyncAndAdmission(t *testing.T) {
	s := testStore(t)
	s.maxDiskFiles = 2
	writeRetiredJournal(t, s, "old.service", "old")
	if err := s.append(Entry{Unit: "independent.service", Message: "already accepted"}); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	s.onRetentionRemove = func(string) error { close(entered); <-release; return nil }
	done := make(chan error, 1)
	go func() { done <- s.append(Entry{Unit: "new.service", Message: "new"}) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("retention did not enter")
	}
	progress := make(chan error, 1)
	go func() {
		if err := s.RetainNames([]string{"independent.service"}); err != nil {
			progress <- err
			return
		}
		_ = s.TotalCaptureStats()
		if _, err := s.file("old.service"); !errors.Is(err, errRetentionBusy) {
			progress <- errors.New("new file admission raced historical deletion")
			return
		}
		progress <- s.syncAndRetireUnit("independent.service")
	}()
	select {
	case err := <-progress:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("retention held queue or independent sync")
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := s.syncAndRetireUnit("new.service"); err != nil {
		t.Fatal(err)
	}
	assertDiskAccounting(t, s)
}

func TestDiskRetentionTracksEveryRotationStep(t *testing.T) {
	s := testStore(t)
	s.maxSize = 1
	for i := 0; i < 8; i++ {
		writeRetiredJournal(t, s, "rotate.service", "rotation")
		assertDiskAccounting(t, s)
	}
	s.maxDiskFiles = 4
	writeRetiredJournal(t, s, "new.service", "after rotation")
	if s.TotalCaptureStats().EvictedFiles != 4 {
		t.Fatalf("historical generations not evicted together: %+v", s.TotalCaptureStats())
	}
	assertDiskAccounting(t, s)
}

func TestDiskRetentionRejectsNonregularHistoryAndRecovers(t *testing.T) {
	s := testStore(t)
	if err := os.Mkdir(s.path("unsafe.service"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.append(Entry{Unit: "work.service", Message: "rejected"}); err == nil || !strings.Contains(err.Error(), "nonregular") {
		t.Fatal("unsafe history accepted", err)
	}
	if err := os.Remove(s.path("unsafe.service")); err != nil {
		t.Fatal(err)
	}
	s.diskMu.Lock()
	s.disk.retryAt = time.Time{}
	s.diskMu.Unlock()
	writeRetiredJournal(t, s, "work.service", "after repair")
	assertDiskAccounting(t, s)
}

func TestDiskRetentionPartialEvictionDoesNotRepeatSuccessfulDeletes(t *testing.T) {
	s := testStore(t)
	s.maxSize = 1
	for i := 0; i < 4; i++ {
		writeRetiredJournal(t, s, "old.service", "history")
	}
	s.maxDiskFiles = 1
	calls := make(map[string]int)
	fail := true
	s.onRetentionRemove = func(name string) error {
		calls[name]++
		if strings.HasSuffix(name, ".1") && fail {
			return errors.New("blocked archive")
		}
		return nil
	}
	for i := 0; i < 3; i++ {
		if err := s.append(Entry{Unit: "new.service", Message: "not yet"}); err == nil {
			t.Fatal("partial eviction reported room")
		}
	}
	if calls[unitFileName("old.service")] != 1 || s.TotalCaptureStats().EvictedFiles != 1 {
		t.Fatal("successful deletion repeated", calls, s.TotalCaptureStats())
	}
	fail = false
	writeRetiredJournal(t, s, "new.service", "accepted")
	if s.TotalCaptureStats().EvictedFiles != 4 {
		t.Fatal("eviction accounting lost", s.TotalCaptureStats())
	}
	assertDiskAccounting(t, s)
}

func TestDiskRetentionUsesMostRecentUnitGeneration(t *testing.T) {
	dir := t.TempDir()
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, name := range []string{"recent.service.log.1", "older.service.log", "recent.service.log"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("history\n"), 0600); err != nil {
			t.Fatal(err)
		}
		when := old.Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.maxDiskFiles = 3
	writeRetiredJournal(t, s, "new.service", "new")
	if _, err := os.Stat(s.path("older.service")); !os.IsNotExist(err) {
		t.Fatal("old archive displaced recently used unit", err)
	}
	if _, err := os.Stat(s.path("recent.service")); err != nil {
		t.Fatal("recent current file evicted", err)
	}
	assertDiskAccounting(t, s)
}

func TestDiskRetentionBoundsExistingIndex(t *testing.T) {
	s := testStore(t)
	for i := 0; i <= DefaultMaxDiskFiles; i++ {
		if err := os.WriteFile(s.path(fmt.Sprintf("history-%d.service", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.append(Entry{Unit: "new.service", Message: "rejected"}); err == nil || !strings.Contains(err.Error(), "index exceeds") {
		t.Fatal("oversized index accepted", err)
	}
	s.diskMu.Lock()
	count := len(s.disk.files)
	ready := s.disk.ready
	s.diskMu.Unlock()
	if count != 0 || ready {
		t.Fatal("partial oversized index published", count, ready)
	}
	if _, err := os.Stat(s.path("new.service")); !os.IsNotExist(err) {
		t.Fatal("oversized history admitted new file", err)
	}
}

func TestDiskRetentionReconcilesManuallyRemovedHistory(t *testing.T) {
	s := testStore(t)
	s.maxDiskFiles = 1
	writeRetiredJournal(t, s, "old.service", "old")
	if err := os.Remove(s.path("old.service")); err != nil {
		t.Fatal(err)
	}
	writeRetiredJournal(t, s, "new.service", "new")
	if s.TotalCaptureStats().EvictedFiles != 0 {
		t.Fatal("manual removal charged as retention")
	}
	assertDiskAccounting(t, s)
}

func TestDiskRetentionFallbackNameCannotAliasLiveOwnership(t *testing.T) {
	s := testStore(t)
	writeRetiredJournal(t, s, "_unknown", "standalone")
	for _, name := range []string{".", ".."} {
		if err := s.append(Entry{Unit: name, Message: "ambiguous"}); err == nil {
			t.Fatal("filename alias admitted", name)
		}
	}
	if unit, ok := retainedDiskName("_unknown.log"); !ok || unit != "_unknown" {
		t.Fatal("fallback history not indexed")
	}
	entries, err := s.Read("_unknown")
	if err != nil || len(entries) != 1 || entries[0].Message != "standalone" {
		t.Fatal("aliased history changed", err)
	}
	assertDiskAccounting(t, s)
}
