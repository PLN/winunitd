package manager

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/journal"
)

func TestJournalCleanupRetainsRemovedRecordAndClose(t *testing.T) {
	launch := &hangJournalLauncher{}
	m := managerWith(t, launch, map[string]string{"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\nTimeoutStopSec=50ms\n"})
	t.Cleanup(launch.closePipes)
	if _, err := m.Start(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := m.StopContext(ctx, "work"); err == nil {
		t.Fatal("unfinished output reported stopped")
	}
	waitCond(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.units["work.service"].cleanup&cleanupJournal != 0
	})
	if err := os.Remove(filepath.Join(m.cfg.UnitsDir(), "work.service")); err != nil {
		t.Fatal(err)
	}
	if r, err := m.Reload(); err != nil || len(r.Errors) != 0 {
		t.Fatalf("reload: %+v, %v", r, err)
	}
	st, err := m.Status("work")
	if err != nil || st.Unit.LoadState != "unavailable" || st.Unit.MainPID != 0 {
		t.Fatalf("retained output status: %+v, %v", st, err)
	}
	ctxClose, cancelClose := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancelClose()
	if err := m.CloseContext(ctxClose); err == nil {
		t.Fatal("close forgot open main capture")
	}
	launch.closePipes()
	ctxRetry, cancelRetry := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelRetry()
	if err := m.CloseContext(ctxRetry); err != nil {
		t.Fatalf("close retry: %v", err)
	}
}

func TestStaleJournalCompletionCannotClearReplacement(t *testing.T) {
	old, current := &journal.Capture{}, &journal.Capture{}
	rt := &unitRuntime{capture: current, cleanup: cleanupJournal}
	m := &Manager{units: map[string]*unitRuntime{"work.service": rt}}
	m.publishJournalCompletion("work.service", rt, old, rt.gen, true)
	if rt.capture != current || rt.cleanup&cleanupJournal == 0 {
		t.Fatal("old completion cleared replacement")
	}
	m.publishJournalCompletion("work.service", rt, current, rt.gen, true)
	if rt.capture != nil || rt.cleanupPending() {
		t.Fatal("matching completion retained output")
	}
	rt.gen++
	m.publishJournalCompletion("work.service", rt, nil, rt.gen-1, false)
	if rt.cleanupPending() {
		t.Fatal("old flush-only wait affected a new launch")
	}
}
