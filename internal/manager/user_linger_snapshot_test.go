package manager

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

	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
)

func TestBlockedLingerScanAllowsStatusLogoffAndShutdownDecision(t *testing.T) {
	h, _ := testLingerHost(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	h.cfg.ListLinger = func() ([]runtime.LingerRecord, error) {
		close(entered)
		<-release
		return []runtime.LingerRecord{{SID: testSIDA}}, nil
	}
	done := make(chan struct{})
	go func() { h.StartLingering(); close(done) }()
	awaitNativeWork(t, entered)
	c := &Control{Units: testManager(t, nil), Users: h}
	observed := make(chan error, 1)
	go func() {
		result, err := c.Handle(context.Background(), protocol.MethodStatus, nil)
		if err == nil {
			s := result.(*protocol.StatusResult).Machine
			if s.LingerState != "pending" || s.UserNativeWork != 1 {
				err = fmt.Errorf("unexpected snapshot: %+v", s)
			}
		}
		h.Logoff(1)
		observed <- err
	}()
	if err := waitErr(t, observed); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := h.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("scan ownership escaped shutdown: %v", err)
	}
	if _, err := h.EnableLinger(testSIDA); err == nil {
		t.Fatal("shutdown did not close admission")
	}
	unblock()
	awaitNativeWork(t, done)
	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.LingerCount() != 0 || h.ManagerCount() != 0 {
		t.Fatal("late scan published after shutdown")
	}
}

func TestLingerScanSerializesWithMutation(t *testing.T) {
	h, store := testLingerHost(t)
	defer h.Close()
	rec := runtime.LingerRecord{SID: testSIDA, Name: "alice"}
	if err := store.Put(rec); err != nil {
		t.Fatal(err)
	}
	if _, err := h.refreshLingerRecords(); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	h.cfg.ListLinger = func() ([]runtime.LingerRecord, error) {
		close(entered)
		<-release
		return []runtime.LingerRecord{rec}, nil
	}
	scan := make(chan struct{})
	go func() { h.StartLingering(); close(scan) }()
	awaitNativeWork(t, entered)
	mutation := make(chan error, 1)
	go func() { _, err := h.DisableLinger(testSIDA); mutation <- err }()
	waitCond(t, func() bool { return h.NativeWorkCount() == 2 })
	unblock()
	awaitNativeWork(t, scan)
	if err := waitErr(t, mutation); err != nil {
		t.Fatal(err)
	}
	if h.Lingering(testSIDA) || store.Has(testSIDA) {
		t.Fatal("older scan overwrote completed revocation")
	}
}

func TestInvalidLingerObservationRevokesCachedGrant(t *testing.T) {
	h, store := testLingerHost(t)
	defer h.Close()
	if _, err := h.EnableLinger(testSIDA); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.dir, testSIDA), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	h.StartLingering()
	s := h.decisionSnapshot()
	if s.lingering != 0 || s.lingerState != "degraded" || s.lingerError == "" {
		t.Fatalf("invalid authority retained: %+v", s)
	}
	waitCond(t, func() bool { return h.ManagerCount() == 0 })
	if err := store.Delete(testSIDA); err != nil {
		t.Fatal(err)
	}
	h.StartLingering()
	if s := h.decisionSnapshot(); s.lingerState != "ready" || s.lingerError != "" {
		t.Fatalf("repair not observed: %+v", s)
	}
}

func TestLingerStoreBounds(t *testing.T) {
	t.Run("bytes", func(t *testing.T) {
		s := OpenLingerStore(t.TempDir())
		rec := runtime.LingerRecord{SID: testSIDA, Name: strings.Repeat("x", maxLingerRecordBytes)}
		if err := s.Put(rec); err == nil {
			t.Fatal("oversized write accepted")
		}
		if err := os.WriteFile(s.path(testSIDA), []byte(rec.Name), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Get(testSIDA); err == nil {
			t.Fatal("oversized read accepted")
		}
		if records, err := s.List(); err == nil || len(records) != 0 {
			t.Fatal("invalid record silently accepted")
		}
	})
	t.Run("records", func(t *testing.T) {
		s := OpenLingerStore(t.TempDir())
		for i := 0; i < maxTrackedUserManagers; i++ {
			if err := s.Put(runtime.LingerRecord{SID: fmt.Sprintf("S-1-5-21-1-2-3-%d", 1000+i)}); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Put(runtime.LingerRecord{SID: "S-1-5-21-1-2-3-9999"}); err == nil {
			t.Fatal("record cap bypassed")
		}
		if err := os.WriteFile(s.path("S-1-5-21-1-2-3-9999"), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
		if records, err := s.List(); err == nil || len(records) != 0 {
			t.Fatal("oversized directory was partially accepted")
		}
	})
	t.Run("directory", func(t *testing.T) {
		s := OpenLingerStore(t.TempDir())
		for i := 0; i <= maxLingerDirectoryEntries; i++ {
			if err := os.WriteFile(filepath.Join(s.dir, fmt.Sprintf("unrelated-%d", i)), nil, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if records, err := s.List(); err == nil || len(records) != 0 {
			t.Fatal("directory entry cap bypassed")
		}
	})
}

func TestLingerFailedReplacementPreservesAcceptedRecord(t *testing.T) {
	s := OpenLingerStore(t.TempDir())
	rec := runtime.LingerRecord{SID: testSIDA, Name: "alice"}
	if err := s.Put(rec); err != nil {
		t.Fatal(err)
	}
	oversized := rec
	oversized.Name = strings.Repeat("x", maxLingerRecordBytes)
	if err := s.Put(oversized); err == nil {
		t.Fatal("oversized replacement succeeded")
	}
	if got, err := s.Get(testSIDA); err != nil || got != rec {
		t.Fatalf("failed replacement damaged record: %+v %v", got, err)
	}
	rec.Name = "renamed"
	if err := s.Put(rec); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(testSIDA); err != nil || got != rec {
		t.Fatalf("replacement: %+v %v", got, err)
	}
}

func TestLingerTokenRejectedAfterIdenticalGrantReplacement(t *testing.T) {
	h, _ := testLingerHost(t)
	defer h.Close()
	rec, err := h.mutateLingerRecord(testSIDA, true)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	h.cfg.LingerToken = func(runtime.LingerRecord) (*runtime.UserToken, error) {
		close(entered)
		<-release
		return &runtime.UserToken{Info: runtime.UserInfo{SID: testSIDA}}, nil
	}
	done := make(chan error, 1)
	go func() { done <- h.startLinger(rec) }()
	awaitNativeWork(t, entered)
	if _, err := h.mutateLingerRecord(testSIDA, false); err != nil {
		t.Fatal(err)
	}
	if restored, err := h.mutateLingerRecord(testSIDA, true); err != nil || restored != rec {
		t.Fatalf("replace: %+v %v", restored, err)
	}
	unblock()
	if err := waitErr(t, done); err == nil {
		t.Fatal("superseded token accepted for identical record")
	}
	if h.ManagerCount() != 0 {
		t.Fatal("stale token launched manager")
	}
}
