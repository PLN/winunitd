package manager

import (
	"path/filepath"
	"testing"
)

func TestMachineStatusReportsUnitDirectory(t *testing.T) {
	dir := t.TempDir()
	m, err := New(Config{BaseDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	st, err := m.Status("")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "units"); st.Machine == nil || st.Machine.UnitDirectory != want {
		t.Fatalf("machine status %+v, want unit directory %s", st.Machine, want)
	}
	snap, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snap.Machine.UnitDirectory != "" {
		t.Fatal("snapshot carries the unit directory")
	}
}
