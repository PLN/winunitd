package manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PLN/winunitd/internal/unit"
)

func TestReloadUnitLimitRetainsRemovedWorkload(t *testing.T) {
	m := testManager(t, map[string]string{"old.service": "[Service]\nExecStart=C:\\Tools\\old.exe\n"})
	if _, err := m.Start(context.Background(), "old"); err != nil {
		t.Fatal(err)
	}
	before, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(m.cfg.UnitsDir(), "old.service")); err != nil {
		t.Fatal(err)
	}
	// The candidate fits alone; the removed live workload occupies one slot.
	for i := 0; i < maxManagedUnits-len(builtinTargets); i++ {
		writeUnit(t, m.cfg.UnitsDir(), fmt.Sprintf("u%04d.target", i), "[Unit]\nDescription=bounded\n")
	}
	if _, err := m.Reload(); err == nil || !strings.Contains(err.Error(), "retained ownership") {
		t.Fatalf("reload: %v", err)
	}
	after, err := m.Snapshot()
	if err != nil || len(after.Units) != len(before.Units) {
		t.Fatalf("accepted records changed: %v", err)
	}
	st, err := m.Status("old")
	if err != nil || st.Unit.ActiveState != "active" || st.Unit.ConfigRevision != before.Units[0].ConfigRevision {
		t.Fatalf("old ownership changed: %+v, %v", st, err)
	}
	if _, err := m.Stop("old"); err != nil {
		t.Fatal(err)
	}
	if r, err := m.Reload(); err != nil || len(r.Errors) != 0 {
		t.Fatalf("capacity not released: %+v, %v", r, err)
	}
	after, err = m.Snapshot()
	if err != nil || len(after.Units) != maxManagedUnits {
		t.Fatalf("boundary rejected: %v", err)
	}
	writeUnit(t, m.cfg.UnitsDir(), "overflow.target", "[Unit]\n")
	if _, err := m.Reload(); err == nil {
		t.Fatal("builtin-inclusive overflow accepted")
	}
}

func TestConfigurationEnumerationLimit(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"c", "a", "b"} {
		writeUnit(t, dir, name, "")
	}
	if entries, err := readConfigurationDirectory(dir, 2); err == nil || entries != nil {
		t.Fatal("partial enumeration accepted")
	}
	entries, err := readConfigurationDirectory(dir, 3)
	if err != nil || len(entries) != 3 || entries[0].Name() != "a" {
		t.Fatalf("boundary/order: %v, %v", entries, err)
	}
	if _, err := readConfigurationDirectory(dir, 0); err == nil {
		t.Fatal("zero allowance accepted entries")
	}
}

func TestReloadBoundsFilesAndDiagnostics(t *testing.T) {
	m := testManager(t, map[string]string{"old.target": "[Unit]\nDescription=accepted\n"})
	writeUnit(t, m.cfg.UnitsDir(), "huge.target", strings.Repeat("#", unit.MaxFileBytes+1))
	r, err := m.Reload()
	if err != nil || len(r.Errors) == 0 || !strings.Contains(r.Errors[0], "exceeds") {
		t.Fatalf("oversize file: %+v, %v", r, err)
	}
	writeUnit(t, m.cfg.UnitsDir(), "huge.target", "[Unit]\n"+strings.Repeat("Unknown=invalid\n", 200))
	r, err = m.Reload()
	if err != nil || len(r.Errors) != maxReloadErrors+1 || !strings.Contains(r.Errors[len(r.Errors)-1], "omitted") {
		t.Fatalf("diagnostic bound: %+v, %v", r, err)
	}
	st, err := m.Status("old.target")
	if err != nil || st.Unit.Description != "accepted" {
		t.Fatal("invalid candidate changed accepted configuration")
	}
}

func TestEnabledEnumerationAggregateLimit(t *testing.T) {
	m := testManager(t, nil)
	for _, target := range []string{"a.target", "b.target"} {
		dir := filepath.Join(m.cfg.EnabledDir(), target)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < maxConfigurationEntries/2; i++ {
			writeUnit(t, dir, fmt.Sprintf("u%04d.target", i), "")
		}
	}
	if _, err := m.readEnabledLinks(); err == nil {
		t.Fatal("aggregate enabled entries exceeded budget")
	}
}
