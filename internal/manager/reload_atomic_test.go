package manager

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestInvalidReloadRetainsWholeAcceptedConfiguration(t *testing.T) {
	m := managerWith(t, &fakeLauncher{}, map[string]string{
		"old.service":    "[Unit]\nDescription=accepted\n[Service]\nExecStart=C:\\Tools\\old.exe\n",
		"remove.service": "[Service]\nExecStart=C:\\Tools\\remove.exe\n",
	})
	m.mu.Lock()
	oldGraph, oldRecord, oldDefinition := m.graph, m.units["old.service"], m.units["old.service"].unit
	m.mu.Unlock()
	writeUnit(t, m.cfg.UnitsDir(), "old.service", "[Unit]\nDescription=replacement\n[Service]\nExecStart=C:\\Tools\\new.exe\n")
	writeUnit(t, m.cfg.UnitsDir(), "new.service", "[Service]\nExecStart=C:\\Tools\\new.exe\n")
	writeUnit(t, m.cfg.UnitsDir(), "invalid.service", "[Service]\nType=invalid\n")
	if err := os.Remove(filepath.Join(m.cfg.UnitsDir(), "remove.service")); err != nil {
		t.Fatal(err)
	}
	result, err := m.Reload()
	if err != nil || len(result.Errors) == 0 {
		t.Fatalf("missing candidate diagnostic: %v, %v", result, err)
	}
	m.mu.Lock()
	unchanged := m.graph == oldGraph && m.units["old.service"] == oldRecord && oldRecord.unit == oldDefinition && m.units["remove.service"] != nil && m.units["new.service"] == nil
	m.mu.Unlock()
	if !unchanged {
		t.Fatal("invalid candidate partially replaced the accepted configuration")
	}
	if _, err := m.Start(context.Background(), "remove.service"); err != nil {
		t.Fatalf("rejected removal changed start eligibility: %v", err)
	}
	if err := os.Remove(filepath.Join(m.cfg.UnitsDir(), "invalid.service")); err != nil {
		t.Fatal(err)
	}
	if result, err := m.Reload(); err != nil || len(result.Errors) != 0 {
		t.Fatalf("valid replacement rejected: %v, %v", result, err)
	}
	status, err := m.Status("old.service")
	if err != nil || status.Unit.Description != "replacement" {
		t.Fatal("valid replacement was not accepted")
	}
	status, err = m.Status("remove.service")
	if err != nil || status.Unit.LoadState != "unavailable" {
		t.Fatal("accepted removal did not retain the live runtime")
	}
}

func TestInvalidColdLoadAdmitsNoCandidateUnits(t *testing.T) {
	m, err := New(Config{BaseDir: t.TempDir(), Launch: &fakeLauncher{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	if err := os.MkdirAll(m.cfg.UnitsDir(), 0700); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, m.cfg.UnitsDir(), "valid.service", "[Service]\nExecStart=C:\\Tools\\valid.exe\n")
	writeUnit(t, m.cfg.UnitsDir(), "invalid.service", "[Service]\nType=invalid\n")
	result, err := m.Reload()
	if err != nil || len(result.Errors) == 0 {
		t.Fatal("cold load did not report invalid candidate")
	}
	if _, err := m.Start(context.Background(), "valid.service"); err == nil {
		t.Fatal("cold load admitted the valid subset of a rejected candidate")
	}
	list, err := m.ListUnits()
	if err != nil || len(list.Units) != 0 {
		t.Fatal("control did not expose the empty accepted configuration")
	}
}

func TestCyclicReloadRetainsAcceptedGraph(t *testing.T) {
	m := managerWith(t, &fakeLauncher{}, map[string]string{
		"a.target": "[Unit]\nWants=b.target\nAfter=b.target\n",
		"b.target": "[Unit]\nDescription=accepted\n",
	})
	m.mu.Lock()
	accepted := m.graph
	m.mu.Unlock()
	writeUnit(t, m.cfg.UnitsDir(), "b.target", "[Unit]\nAfter=a.target\n")
	result, err := m.Reload()
	if err != nil || result.Cycle == "" {
		t.Fatal("cyclic candidate was not diagnosed")
	}
	m.mu.Lock()
	unchanged := m.graph == accepted
	m.mu.Unlock()
	if !unchanged {
		t.Fatal("cyclic candidate replaced the accepted graph")
	}
	if _, err := m.Start(context.Background(), "a.target"); err != nil {
		t.Fatalf("rejected cycle changed accepted plan: %v", err)
	}
}

func TestUnreadableConfigurationDirectoryRejectsReload(t *testing.T) {
	for _, source := range []string{"units", "enabled"} {
		t.Run(source, func(t *testing.T) {
			m := managerWith(t, &fakeLauncher{}, map[string]string{
				"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n",
			})
			if _, err := m.Enable("work.service"); err != nil {
				t.Fatal(err)
			}
			m.mu.Lock()
			accepted := m.graph
			m.mu.Unlock()
			directory := m.cfg.EnabledDir()
			if source == "units" {
				directory = m.cfg.UnitsDir()
			}
			if err := os.Rename(directory, directory+"-saved"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(directory, []byte("not a directory"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Reload(); err == nil {
				t.Fatal("unreadable configuration directory was treated as empty configuration")
			}
			m.mu.Lock()
			unchanged := m.graph == accepted && m.units["work.service"].enabled
			m.mu.Unlock()
			if !unchanged {
				t.Fatal("failed configuration read changed accepted graph or enablement")
			}
		})
	}
}
