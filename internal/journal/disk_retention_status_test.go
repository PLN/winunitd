package journal_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PLN/winunitd/internal/journal"
	"github.com/PLN/winunitd/internal/manager"
)

func TestDiskRetentionManagerStatusPreservesEvictionTotals(t *testing.T) {
	base := t.TempDir()
	units := filepath.Join(base, "units")
	if err := os.MkdirAll(units, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(units, "history.service")
	if err := os.WriteFile(path, []byte("[Service]\nExecStart=C:\\Tools\\worker.exe\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var store *journal.Store
	m, err := manager.New(manager.Config{BaseDir: base, JournalOpen: func(dir string) (*journal.Store, error) {
		var openErr error
		store, openErr = journal.OpenRetentionTestStore(dir, 1)
		return store, openErr
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if result, err := m.Reload(); err != nil || len(result.Errors) != 0 {
		t.Fatal("initial reload failed", result, err)
	}
	for _, name := range []string{"history.service", "next.service"} {
		c := store.Attach(name, 42, "retention", strings.NewReader("history\n"), nil)
		if !c.WaitContext(context.Background()) {
			t.Fatal("capture failed", name)
		}
	}
	unit, err := m.Status("history")
	if err != nil || unit.Unit.LogEvictedFiles != 1 || unit.Unit.LogEvictedBytes == 0 || unit.Unit.LogDroppedRecords != 0 || unit.Unit.LogStorageErrors != 0 {
		t.Fatalf("eviction did not reach unit status: %+v %v", unit, err)
	}
	wantBytes := unit.Unit.LogEvictedBytes
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if result, err := m.Reload(); err != nil || len(result.Errors) != 0 {
		t.Fatal("reload failed", result, err)
	}
	machine, err := m.Status("")
	if err != nil || machine.Machine.LogEvictedFiles != 1 || machine.Machine.LogEvictedBytes != wantBytes || machine.Machine.LogDroppedRecords != 0 || machine.Machine.LogStorageErrors != 0 {
		t.Fatalf("eviction totals lost after reload: %+v %v", machine, err)
	}
}
