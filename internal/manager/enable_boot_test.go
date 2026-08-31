package manager

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/unit"
)

func TestBuiltinTargetsLoaded(t *testing.T) {
	t.Parallel()
	m := testManager(t, nil)
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, name := range []string{DefaultTarget, TimersTarget, ShutdownTarget} {
		ld, ok := m.units[name]
		if !ok || ld.unit.Kind != unit.KindTarget {
			t.Fatalf("missing builtin %s: %+v", name, ld)
		}
	}
	if _, ok := m.units["network-online.target"]; ok {
		t.Fatal("network-online.target must not be shipped")
	}
}

func TestDiskTargetOverridesBuiltin(t *testing.T) {
	t.Parallel()
	m := testManager(t, map[string]string{
		"default.target": `
[Unit]
Description=Custom default
`,
	})
	st, err := m.Status("default.target")
	if err != nil {
		t.Fatal(err)
	}
	if st.Unit == nil || st.Unit.Description != "Custom default" {
		t.Fatalf("status = %+v", st.Unit)
	}
	if st.Unit.Path == "" || st.Unit.Path == "builtin:"+DefaultTarget {
		t.Fatalf("disk unit should keep its path: %+v", st.Unit)
	}
}

func TestEnableWithoutWantedByUsesDefaultTarget(t *testing.T) {
	t.Parallel()
	m := testManager(t, map[string]string{
		"foo.service": `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
	})
	en, err := m.Enable("foo")
	if err != nil {
		t.Fatal(err)
	}
	if !en.Enabled || len(en.Targets) != 1 || en.Targets[0] != DefaultTarget {
		t.Fatalf("enable = %+v", en)
	}
	assertEnableFile(t, m.cfg.EnabledPath(DefaultTarget, "foo.service"), "foo.service")
}

func TestEnableFileIsRegularNotSymlink(t *testing.T) {
	t.Parallel()
	m := testManager(t, map[string]string{
		"hermes.service": `
[Service]
ExecStart=C:\Tools\hermes.exe
WorkingDirectory=C:\Tools
[Install]
WantedBy=default.target
`,
	})
	if _, err := m.Enable("hermes.service"); err != nil {
		t.Fatal(err)
	}
	path := m.cfg.EnabledPath(DefaultTarget, "hermes.service")
	assertEnableFile(t, path, "hermes.service")

	if _, err := m.Disable("hermes.service"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("disable left file: %v", err)
	}
}

func TestEnableTimersTarget(t *testing.T) {
	t.Parallel()
	m := testManager(t, map[string]string{
		"backup.timer": `
[Timer]
OnCalendar=daily
[Install]
WantedBy=timers.target
`,
		"backup.service": `
[Service]
ExecStart=C:\Tools\backup.exe
WorkingDirectory=C:\Tools
`,
	})
	en, err := m.Enable("backup.timer")
	if err != nil {
		t.Fatal(err)
	}
	if len(en.Targets) != 1 || en.Targets[0] != TimersTarget {
		t.Fatalf("enable = %+v", en)
	}
	assertEnableFile(t, m.cfg.EnabledPath(TimersTarget, "backup.timer"), "backup.timer")
	m.mu.Lock()
	wants := m.graph.Wants(TimersTarget)
	m.mu.Unlock()
	if !containsString(wants, "backup.timer") {
		t.Fatalf("timers.target wants = %v", wants)
	}
}

func TestEnableDoesNotStart(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	m := managerWith(t, launch, map[string]string{
		"foo.service": `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
[Install]
WantedBy=default.target
`,
	})
	if _, err := m.Enable("foo"); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "foo.service", core.Inactive)
	if specs := launch.specs(); len(specs) != 0 {
		t.Fatalf("enable must not start: %+v", specs)
	}
}

func TestBootStartsOnlyEnabledUnits(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	m := managerWith(t, launch, map[string]string{
		"on.service": `
[Service]
ExecStart=C:\Tools\on.exe
WorkingDirectory=C:\Tools
[Install]
WantedBy=default.target
`,
		"off.service": `
[Service]
ExecStart=C:\Tools\off.exe
WorkingDirectory=C:\Tools
[Install]
WantedBy=default.target
`,
		"also.service": `
[Service]
ExecStart=C:\Tools\also.exe
WorkingDirectory=C:\Tools
`,
	})
	if _, err := m.Enable("on.service"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, DefaultTarget, core.Active)
	assertState(t, m, TimersTarget, core.Active)
	assertState(t, m, "on.service", core.Active)
	assertState(t, m, "off.service", core.Inactive)
	assertState(t, m, "also.service", core.Inactive)
	got := launch.units()
	if len(got) != 1 || got[0] != "on.service" {
		t.Fatalf("boot started %v, want only on.service", got)
	}
}

func TestStartDefaultTargetPullsEnabledWants(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	m := managerWith(t, launch, map[string]string{
		"web.service": `
[Unit]
Requires=db.service
After=db.service
[Service]
ExecStart=C:\Tools\web.exe
WorkingDirectory=C:\Tools
[Install]
WantedBy=default.target
`,
		"db.service": `
[Service]
ExecStart=C:\Tools\db.exe
WorkingDirectory=C:\Tools
`,
	})
	if _, err := m.Enable("web"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(context.Background(), DefaultTarget); err != nil {
		t.Fatal(err)
	}
	got := launch.units()
	if len(got) != 2 || got[0] != "db.service" || got[1] != "web.service" {
		t.Fatalf("start order = %v", got)
	}
}

func TestDaemonReloadKeepsRunningJob(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, units, "foo.service", `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`)
	m, err := New(Config{BaseDir: dir, Launch: launch})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	if _, err := m.Start(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	before := m.procs["foo.service"]
	m.mu.Unlock()
	if before == nil || !before.Alive() {
		t.Fatal("expected a live job before reload")
	}

	writeUnit(t, units, "foo.service", `
[Unit]
Description=Reloaded foo
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`)
	writeUnit(t, units, "bar.service", `
[Service]
ExecStart=C:\Tools\bar.exe
WorkingDirectory=C:\Tools
`)
	rel, err := m.Reload()
	if err != nil {
		t.Fatal(err)
	}
	if rel.Loaded < 5 {
		t.Fatalf("reload = %+v", rel)
	}
	st, err := m.Status("foo")
	if err != nil {
		t.Fatal(err)
	}
	if st.Unit == nil || st.Unit.Description != "Reloaded foo" || st.Unit.ActiveState != "active" {
		t.Fatalf("foo after reload = %+v", st.Unit)
	}
	m.mu.Lock()
	after := m.procs["foo.service"]
	bar := m.procs["bar.service"]
	m.mu.Unlock()
	if after != before {
		t.Fatal("daemon-reload dropped the live job")
	}
	if !after.Alive() {
		t.Fatal("live job died during reload")
	}
	if bar != nil {
		t.Fatal("reload must not start newly loaded units")
	}
	assertState(t, m, "bar.service", core.Inactive)
}

func TestEnableRebuildsGraphWithoutReload(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	m := managerWith(t, launch, map[string]string{
		"foo.service": `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
[Install]
WantedBy=default.target
`,
	})
	m.mu.Lock()
	before := append([]string(nil), m.graph.Wants(DefaultTarget)...)
	m.mu.Unlock()
	if containsString(before, "foo.service") {
		t.Fatalf("not yet enabled, wants = %v", before)
	}
	if _, err := m.Enable("foo"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	after := m.graph.Wants(DefaultTarget)
	m.mu.Unlock()
	if !containsString(after, "foo.service") {
		t.Fatalf("after enable, wants = %v", after)
	}
}

func assertEnableFile(t *testing.T, path, unitName string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("enable file %s: %v", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("enable file must not be a symlink: %s", path)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("enable file must be a regular file: %s mode=%v", path, info.Mode())
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != unitName+"\n" {
		t.Fatalf("enable file body = %q", body)
	}
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
