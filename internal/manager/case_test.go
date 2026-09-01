package manager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PLN/winunitd/internal/protocol"
)

const simpleService = `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`

func TestStatusFOOEqualsFoo(t *testing.T) {
	t.Parallel()
	m := testManager(t, map[string]string{
		"foo.service": `
[Unit]
Description=Foo
` + simpleService,
	})
	lower, err := m.Status("foo")
	if err != nil {
		t.Fatal(err)
	}
	upper, err := m.Status("FOO")
	if err != nil {
		t.Fatal(err)
	}
	if lower.Unit == nil || upper.Unit == nil {
		t.Fatalf("status = %+v / %+v", lower.Unit, upper.Unit)
	}
	if lower.Unit.Name != "foo.service" || upper.Unit.Name != "foo.service" {
		t.Fatalf("names = %q / %q", lower.Unit.Name, upper.Unit.Name)
	}
	if lower.Unit.Description != "Foo" || upper.Unit.Description != "Foo" {
		t.Fatalf("desc = %q / %q", lower.Unit.Description, upper.Unit.Description)
	}
	if lower.Unit.Path != upper.Unit.Path || !strings.Contains(lower.Unit.Path, "foo.service") {
		t.Fatalf("path = %q / %q", lower.Unit.Path, upper.Unit.Path)
	}
}

func TestMixedCaseFileNameKeepsPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, units, "FOO.service", `
[Unit]
Description=Mixed
`+simpleService)
	m, err := New(Config{BaseDir: dir, Launch: &fakeLauncher{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	st, err := m.Status("foo")
	if err != nil {
		t.Fatal(err)
	}
	if st.Unit == nil || st.Unit.Name != "foo.service" {
		t.Fatalf("status = %+v", st.Unit)
	}
	if !strings.Contains(st.Unit.Path, "FOO.service") && !strings.Contains(st.Unit.Path, "foo.service") {
		t.Fatalf("path should keep on-disk file: %q", st.Unit.Path)
	}
}

func TestRequiresMixedCaseResolvesLoadedUnit(t *testing.T) {
	t.Parallel()
	m := testManager(t, map[string]string{
		"web.service": `
[Unit]
Requires=Foo.service
After=Foo.service
` + strings.Replace(simpleService, "foo.exe", "web.exe", 1),
		"foo.service": simpleService,
	})
	got, err := m.Start(context.Background(), "WEB")
	if err != nil {
		t.Fatal(err)
	}
	if got.Unit != "web.service" {
		t.Fatalf("start = %+v", got)
	}
	web, err := m.Status("web")
	if err != nil {
		t.Fatal(err)
	}
	foo, err := m.Status("FOO.SERVICE")
	if err != nil {
		t.Fatal(err)
	}
	if web.Unit.ActiveState != "active" || foo.Unit.ActiveState != "active" {
		t.Fatalf("web=%s foo=%s", web.Unit.ActiveState, foo.Unit.ActiveState)
	}
}

func TestEnableUsesNormalizedName(t *testing.T) {
	t.Parallel()
	m := testManager(t, map[string]string{
		"foo.service": simpleService + `
[Install]
WantedBy=Default.target
`,
	})
	en, err := m.Enable("FOO")
	if err != nil {
		t.Fatal(err)
	}
	if !en.Enabled || en.Unit != "foo.service" {
		t.Fatalf("enable = %+v", en)
	}
	if len(en.Targets) != 1 || en.Targets[0] != DefaultTarget {
		t.Fatalf("targets = %v", en.Targets)
	}
	assertEnableFile(t, m.cfg.EnabledPath(DefaultTarget, "foo.service"), "foo.service")
	assertEnableFile(t, m.cfg.EnabledPath("DEFAULT.target", "FOO.SERVICE"), "foo.service")
}

func TestCaseVariantUnitFilesAreLoadError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, units, "foo.service", simpleService)
	writeUnit(t, units, "Foo.service", simpleService)
	ents, err := os.ReadDir(units)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range ents {
		if strings.EqualFold(e.Name(), "foo.service") {
			n++
		}
	}
	if n < 2 {
		t.Skip("filesystem is case-insensitive; cannot create a case-variant pair")
	}

	m, err := New(Config{BaseDir: dir, Launch: &fakeLauncher{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	rel, err := m.Reload()
	if err != nil {
		t.Fatal(err)
	}
	foundDup := false
	for _, e := range rel.Errors {
		if strings.Contains(e, "duplicate unit") && strings.Contains(e, "foo.service") {
			foundDup = true
			break
		}
	}
	if !foundDup {
		t.Fatalf("reload errors = %v, want duplicate unit foo.service", rel.Errors)
	}

	list, err := m.ListUnits()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, u := range list.Units {
		if strings.EqualFold(u.Name, "foo.service") {
			count++
		}
	}
	if count > 1 {
		t.Fatalf("loaded %d case-variant units", count)
	}
}

func TestEnableWantedByMixedCase(t *testing.T) {
	t.Parallel()
	m := testManager(t, map[string]string{
		"app.service": simpleService + `
[Install]
WantedBy=Timers.target
`,
	})
	en, err := m.Enable("App")
	if err != nil {
		t.Fatal(err)
	}
	if en.Unit != "app.service" || len(en.Targets) != 1 || en.Targets[0] != TimersTarget {
		t.Fatalf("enable = %+v", en)
	}
	assertEnableFile(t, m.cfg.EnabledPath(TimersTarget, "app.service"), "app.service")
}

func TestStatusUnknownMixedCaseIsNotFound(t *testing.T) {
	t.Parallel()
	m := testManager(t, nil)
	_, err := m.Status("NOPE")
	pe, ok := err.(*protocol.Error)
	if !ok || pe.Code != protocol.CodeNotFound {
		t.Fatalf("err = %v", err)
	}
}
