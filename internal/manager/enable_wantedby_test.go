package manager

import (
	"os"
	"path/filepath"
	"testing"
)

// assertNoEscape fails if an enable link escaped enabled/ into the manager
// base directory or its parent.
func assertNoEscape(t *testing.T, m *Manager, name string) {
	t.Helper()
	for _, dir := range []string{m.cfg.BaseDir, filepath.Dir(m.cfg.BaseDir)} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("enable wrote outside enabled/: %s (%v)", filepath.Join(dir, name), err)
		}
	}
}

// A traversing WantedBy= does not load, so Enable cannot write through it.
func TestEnableRejectsTraversingWantedBy(t *testing.T) {
	t.Parallel()
	m := testManager(t, map[string]string{
		"app.service": simpleService + `
[Install]
WantedBy=..\..\w8-escape.target ../../w8-escape.target
`,
	})
	if _, err := m.Enable("app"); err == nil {
		t.Fatal("Enable accepted a unit with a traversing WantedBy=")
	}
	assertNoEscape(t, m, "w8-escape.target")
}

// Enable rechecks link names itself before writing anything.
func TestEnableRechecksWantedByBeforeWriting(t *testing.T) {
	t.Parallel()
	m := testManager(t, map[string]string{
		"app.service": simpleService + `
[Install]
WantedBy=default.target
`,
	})
	m.mu.Lock()
	rt := m.units["app.service"]
	if rt == nil || rt.unit == nil {
		m.mu.Unlock()
		t.Fatal("app.service did not load")
	}
	// This invalid target sorts after default.target, so validating only
	// inside the write loop would incorrectly write the default link first.
	rt.unit.WantedBy = append(rt.unit.WantedBy, `z/escape.target`)
	m.mu.Unlock()
	if _, err := m.Enable("app"); err == nil {
		t.Fatal("Enable accepted a traversing target")
	}
	assertNoEscape(t, m, "w8-recheck.target")
	if _, err := os.Lstat(m.cfg.EnabledPath(DefaultTarget, "app.service")); !os.IsNotExist(err) {
		t.Fatalf("Enable wrote a link before rejecting the target set: %v", err)
	}
}
