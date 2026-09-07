package manager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/pathwatch"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
)

func TestListUnitsShowsPath(t *testing.T) {
	t.Parallel()
	m := managerWithPath(t, &fakeLauncher{}, newFakePathHub().Open, map[string]string{
		"foo.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.path": `
[Path]
PathChanged=C:\Data\incoming
`,
	})
	list, err := m.ListUnits()
	if err != nil {
		t.Fatal(err)
	}
	var found protocol.UnitStatus
	for _, u := range list.Units {
		if u.Name == "foo.path" {
			found = u
		}
	}
	if found.Name == "" || found.Kind != "path" {
		t.Fatalf("list-units missing .path: %+v", list.Units)
	}
}

func TestPathChangeStartsOneshotOnce(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(0)}
	hub := newFakePathHub()
	m := managerWithPath(t, launch, hub.Open, map[string]string{
		"foo.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.path": `
[Path]
PathChanged=C:\Data\incoming
`,
	})
	if _, err := m.Start(context.Background(), "foo.path"); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "foo.path", core.Active)
	if launch.nstarts() != 0 {
		t.Fatal("watching must not start the oneshot until a change")
	}
	hub.Fire(`C:\Data\incoming`)
	waitStarts(t, launch, 1, 2*time.Second)
	waitUntil(t, 2*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		proc := m.procOfLocked("foo.service")
		return proc == nil || !proc.Alive()
	})
	if _, err := m.Stop("foo.service"); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "foo.service", core.Inactive)
	hub.Fire(`C:\Data\incoming`)
	waitStarts(t, launch, 2, 2*time.Second)
}

func TestPathRepeatableOR(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(0)}
	hub := newFakePathHub()
	m := managerWithPath(t, launch, hub.Open, map[string]string{
		"foo.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.path": `
[Path]
PathChanged=C:\Data\a
PathChanged=C:\Data\b
`,
	})
	if _, err := m.Start(context.Background(), "foo.path"); err != nil {
		t.Fatal(err)
	}
	hub.Fire(`C:\Data\b`)
	waitStarts(t, launch, 1, 2*time.Second)
}

func TestPathUnrelatedPathDoesNotStart(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(0)}
	hub := newFakePathHub()
	m := managerWithPath(t, launch, hub.Open, map[string]string{
		"foo.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.path": `
[Path]
PathChanged=C:\Data\incoming
`,
	})
	if _, err := m.Start(context.Background(), "foo.path"); err != nil {
		t.Fatal(err)
	}
	hub.Fire(`C:\Data\other`)
	// Fake dispatch returns synchronously without sending to an unrelated watch.
	if launch.nstarts() != 0 {
		t.Fatal("unrelated path must not start the oneshot")
	}
}

func TestPathDoesNotRestartRunningSimple(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	hub := newFakePathHub()
	waiting := make(chan struct{}, 8)
	m := managerWithPath(t, launch, func(spec pathwatch.Spec) (pathwatch.Watch, error) {
		w, err := hub.Open(spec)
		if err != nil {
			return nil, err
		}
		return &acknowledgedWatch{watchIO: w, waiting: waiting}, nil
	}, map[string]string{
		"foo.service": `
[Service]
Type=simple
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.path": `
[Path]
PathChanged=C:\Data\incoming
`,
	})
	if _, err := m.Start(context.Background(), "foo.service"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(context.Background(), "foo.path"); err != nil {
		t.Fatal(err)
	}
	if n := len(launch.units()); n != 1 {
		t.Fatalf("starts = %d, want 1", n)
	}
	gen := genOf(t, m, "foo.service")
	waitWatchCycle(t, waiting) // Initial receive is armed.
	hub.Fire(`C:\Data\incoming`)
	waitWatchCycle(t, waiting) // Callback returned and the next receive is armed.
	if n := len(launch.units()); n != 1 {
		t.Fatalf("running simple must not restart; starts = %d", n)
	}
	if got := genOf(t, m, "foo.service"); got != gen {
		t.Fatalf("gen = %d after path change, want %d (C1 must not bump)", got, gen)
	}
}

func TestPathMissingPathFailsConfiguration(t *testing.T) {
	t.Parallel()
	hub := newFakePathHub()
	hub.openErr = pathwatch.ErrMissingPath
	m := managerWithPath(t, &fakeLauncher{}, hub.Open, map[string]string{
		"foo.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.path": `
[Path]
PathChanged=C:\Data\missing
`,
	})
	_, err := m.Start(context.Background(), "foo.path")
	if err == nil {
		t.Fatal("missing path must fail start")
	}
	assertState(t, m, "foo.path", core.Failed)
	st, err := m.Status("foo.path")
	if err != nil || st.Unit == nil {
		t.Fatalf("status = %+v err=%v", st, err)
	}
	if st.Unit.Reason != core.ReasonConfiguration {
		t.Fatalf("reason = %q error=%q", st.Unit.Reason, st.Unit.Error)
	}
	if st.Unit.ActiveState != core.Failed.String() {
		t.Fatalf("state = %s", st.Unit.ActiveState)
	}
	ms, err := m.Status("")
	if err != nil || ms.Machine == nil || ms.Machine.State != "running" {
		t.Fatalf("daemon must stay up: %+v err=%v", ms, err)
	}
}

func TestUserManagerAcceptsPath(t *testing.T) {
	t.Parallel()
	hub := newFakePathHub()
	launch := &scriptedLauncher{exitAll: intPtr(0)}
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, units, "foo.service", `
[Service]
Type=oneshot
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`)
	writeUnit(t, units, "foo.path", `
[Path]
PathChanged=C:\Data\incoming
`)
	m, err := New(Config{BaseDir: dir, Launch: launch, PathOpen: hub.Open, UserScope: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	if _, err := m.Start(context.Background(), "foo.path"); err != nil {
		t.Fatal(err)
	}
	hub.Fire(`C:\Data\incoming`)
	waitStarts(t, launch, 1, 2*time.Second)
}

func TestDisablePathDoesNotStartOneshot(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(0)}
	hub := newFakePathHub()
	m := managerWithPath(t, launch, hub.Open, map[string]string{
		"foo.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.path": `
[Path]
PathChanged=C:\Data\incoming
[Install]
WantedBy=default.target
`,
	})
	if _, err := m.Enable("foo.path"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Disable("foo.path"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "foo.path", core.Inactive)
	hub.Fire(`C:\Data\incoming`)
	// Boot completed synchronously; the disabled watch was never armed.
	if launch.nstarts() != 0 {
		t.Fatal("disabled path unit must not start the oneshot")
	}
}

func TestVerifyPathPairOverPipe(t *testing.T) {
	t.Parallel()
	m := managerWithPath(t, &fakeLauncher{}, newFakePathHub().Open, map[string]string{
		"foo.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.path": `
[Path]
PathChanged=C:\Data\incoming
`,
	})
	client, stop := serveManager(t, m, protocol.AllowAdmin)
	defer stop()
	ver, err := client.Verify(context.Background(), "foo.path")
	if err != nil || !ver.OK {
		t.Fatalf("verify = %+v err=%v", ver, err)
	}
}

func TestPathCloseStopsWatch(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(0)}
	hub := newFakePathHub()
	m := managerWithPath(t, launch, hub.Open, map[string]string{
		"foo.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.path": `
[Path]
PathChanged=C:\Data\incoming
`,
	})
	if _, err := m.Start(context.Background(), "foo.path"); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "foo.path", core.Active)
	m.Close()
	hub.mu.Lock()
	w := hub.byKey[`C:\Data\incoming`]
	hub.mu.Unlock()
	if w == nil || !w.closed {
		t.Fatal("Close must close the path watch")
	}
	if launch.nstarts() != 0 {
		t.Fatal("Close must not start the counterpart")
	}
}

func TestReloadDropsOrphanPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, units, "orphan.path", `
[Path]
PathChanged=C:\Data\incoming
`)
	m, err := New(Config{BaseDir: dir, Launch: &fakeLauncher{}, PathOpen: newFakePathHub().Open})
	if err != nil {
		t.Fatal(err)
	}
	rel, err := m.Reload()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	joined := ""
	for _, e := range rel.Errors {
		joined += e + "\n"
	}
	if !strings.Contains(joined, "missing companion") {
		t.Fatalf("reload errors = %s", joined)
	}
	m.mu.Lock()
	_, ok := m.units["orphan.path"]
	m.mu.Unlock()
	if ok {
		t.Fatal("orphan path without companion must not load")
	}
}

func pathExistsPair() map[string]string {
	return map[string]string{
		"foo.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.path": `
[Path]
PathExists=C:\Data\ready.flag
`,
	}
}

func TestPathExistsMissingDoesNotFailUnit(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(0)}
	hub := newFakePathHub()
	m := managerWithExists(t, launch, hub, pathExistsPair())
	if _, err := m.Start(context.Background(), "foo.path"); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "foo.path", core.Active)
	// Synchronous startup found no satisfied condition and queued no activation.
	if launch.nstarts() != 0 {
		t.Fatal("missing PathExists must wait, not start the oneshot")
	}
}

func TestPathExistsCreateSatisfies(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(0)}
	hub := newFakePathHub()
	m := managerWithExists(t, launch, hub, pathExistsPair())
	if _, err := m.Start(context.Background(), "foo.path"); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "foo.path", core.Active)
	hub.SetExists(`C:\Data\ready.flag`, true)
	waitStarts(t, launch, 1, 2*time.Second)
}

func TestPathExistsAlreadyPresentStartsOnce(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(0)}
	hub := newFakePathHub()
	hub.acknowledgements = map[string]chan struct{}{`C:\Data\ready.flag`: make(chan struct{}, 8)}
	hub.exists[`C:\Data\ready.flag`] = true
	m := managerWithExists(t, launch, hub, pathExistsPair())
	if _, err := m.Start(context.Background(), "foo.path"); err != nil {
		t.Fatal(err)
	}
	waitStarts(t, launch, 1, 2*time.Second)
	waitUntil(t, 2*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		proc := m.procOfLocked("foo.service")
		return proc == nil || !proc.Alive()
	})
	waitWatchCycle(t, hub.acknowledgements[`C:\Data\ready.flag`])
	hub.SetExists(`C:\Data\ready.flag`, true)
	waitWatchCycle(t, hub.acknowledgements[`C:\Data\ready.flag`])
	if launch.nstarts() != 1 {
		t.Fatalf("still-satisfied PathExists must not retrigger; starts = %d", launch.nstarts())
	}
}

func TestPathExistsRepeatableAND(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(0)}
	hub := newFakePathHub()
	hub.acknowledgements = map[string]chan struct{}{`C:\Data\a`: make(chan struct{}, 8), `C:\Data\b`: make(chan struct{}, 8)}
	m := managerWithExists(t, launch, hub, map[string]string{
		"foo.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.path": `
[Path]
PathExists=C:\Data\a
PathExists=C:\Data\b
`,
	})
	if _, err := m.Start(context.Background(), "foo.path"); err != nil {
		t.Fatal(err)
	}
	waitWatchCycle(t, hub.acknowledgements[`C:\Data\a`])
	hub.SetExists(`C:\Data\a`, true)
	waitWatchCycle(t, hub.acknowledgements[`C:\Data\a`])
	if launch.nstarts() != 0 {
		t.Fatal("PathExists AND must not start until every path exists")
	}
	waitWatchCycle(t, hub.acknowledgements[`C:\Data\b`])
	hub.SetExists(`C:\Data\b`, true)
	waitStarts(t, launch, 1, 2*time.Second)
}

func TestPathExistsDeletionDoesNotStopSimple(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	hub := newFakePathHub()
	hub.acknowledgements = map[string]chan struct{}{`C:\Data\ready.flag`: make(chan struct{}, 8)}
	hub.exists[`C:\Data\ready.flag`] = true
	m := managerWithExists(t, launch, hub, map[string]string{
		"foo.service": `
[Service]
Type=simple
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.path": `
[Path]
PathExists=C:\Data\ready.flag
`,
	})
	if _, err := m.Start(context.Background(), "foo.path"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 2*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		proc := m.procOfLocked("foo.service")
		return proc != nil && proc.Alive()
	})
	m.mu.Lock()
	proc := m.procOfLocked("foo.service")
	pid := proc.PID()
	m.mu.Unlock()
	gen := genOf(t, m, "foo.service")
	waitWatchCycle(t, hub.acknowledgements[`C:\Data\ready.flag`])
	hub.SetExists(`C:\Data\ready.flag`, false)
	waitWatchCycle(t, hub.acknowledgements[`C:\Data\ready.flag`])
	m.mu.Lock()
	got := m.procOfLocked("foo.service")
	m.mu.Unlock()
	if got == nil || !got.Alive() {
		t.Fatal("deletion must not stop the counterpart")
	}
	if got.PID() != pid {
		t.Fatalf("deletion must not stop the counterpart; pid %d -> %d", pid, got.PID())
	}
	if gotGen := genOf(t, m, "foo.service"); gotGen != gen {
		t.Fatalf("gen = %d after PathExists delete, want %d", gotGen, gen)
	}
	if n := len(launch.units()); n != 1 {
		t.Fatalf("starts = %d, want 1", n)
	}
}

func TestPathExistsDoesNotRestartRunningSimple(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	hub := newFakePathHub()
	hub.acknowledgements = map[string]chan struct{}{`C:\Data\ready.flag`: make(chan struct{}, 8)}
	m := managerWithExists(t, launch, hub, map[string]string{
		"foo.service": `
[Service]
Type=simple
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.path": `
[Path]
PathExists=C:\Data\ready.flag
`,
	})
	if _, err := m.Start(context.Background(), "foo.service"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(context.Background(), "foo.path"); err != nil {
		t.Fatal(err)
	}
	if n := len(launch.units()); n != 1 {
		t.Fatalf("starts = %d, want 1", n)
	}
	gen := genOf(t, m, "foo.service")
	waitWatchCycle(t, hub.acknowledgements[`C:\Data\ready.flag`])
	hub.SetExists(`C:\Data\ready.flag`, true)
	waitWatchCycle(t, hub.acknowledgements[`C:\Data\ready.flag`])
	if n := len(launch.units()); n != 1 {
		t.Fatalf("running simple must not restart; starts = %d", n)
	}
	if got := genOf(t, m, "foo.service"); got != gen {
		t.Fatalf("gen = %d after PathExists, want %d (C1 must not bump)", got, gen)
	}
}

func TestPathExistsMixChangedORExists(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(0)}
	hub := newFakePathHub()
	m := managerWithExists(t, launch, hub, map[string]string{
		"foo.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.path": `
[Path]
PathChanged=C:\Data\incoming
PathExists=C:\Data\ready.flag
`,
	})
	if _, err := m.Start(context.Background(), "foo.path"); err != nil {
		t.Fatal(err)
	}
	hub.Fire(`C:\Data\incoming`)
	waitStarts(t, launch, 1, 2*time.Second)
	waitUntil(t, 2*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		proc := m.procOfLocked("foo.service")
		return proc == nil || !proc.Alive()
	})
	if _, err := m.Stop("foo.service"); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "foo.service", core.Inactive)
	hub.SetExists(`C:\Data\ready.flag`, true)
	waitStarts(t, launch, 2, 2*time.Second)
}

func TestPathExistsOneshotAgainAfterRecreate(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(0)}
	hub := newFakePathHub()
	hub.exists[`C:\Data\ready.flag`] = true
	m := managerWithExists(t, launch, hub, pathExistsPair())
	if _, err := m.Start(context.Background(), "foo.path"); err != nil {
		t.Fatal(err)
	}
	waitStarts(t, launch, 1, 2*time.Second)
	waitUntil(t, 2*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		proc := m.procOfLocked("foo.service")
		return proc == nil || !proc.Alive()
	})
	if _, err := m.Stop("foo.service"); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "foo.service", core.Inactive)
	hub.SetExists(`C:\Data\ready.flag`, false)
	waitUntil(t, 2*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		rt := m.units["foo.path"]
		return rt != nil && rt.hub != nil && !rt.hub.existsSatisfied
	})
	hub.SetExists(`C:\Data\ready.flag`, true)
	waitStarts(t, launch, 2, 2*time.Second)
}

func TestPathExistsUnwatchableFailsConfiguration(t *testing.T) {
	t.Parallel()
	hub := newFakePathHub()
	hub.existsOpenErr = pathwatch.ErrUnwatchable
	m := managerWithExists(t, &fakeLauncher{}, hub, pathExistsPair())
	_, err := m.Start(context.Background(), "foo.path")
	if err == nil {
		t.Fatal("unwatchable PathExists must fail start")
	}
	assertState(t, m, "foo.path", core.Failed)
	st, err := m.Status("foo.path")
	if err != nil || st.Unit == nil {
		t.Fatalf("status = %+v err=%v", st, err)
	}
	if st.Unit.Reason != core.ReasonConfiguration {
		t.Fatalf("reason = %q error=%q", st.Unit.Reason, st.Unit.Error)
	}
	ms, err := m.Status("")
	if err != nil || ms.Machine == nil || ms.Machine.State != "running" {
		t.Fatalf("daemon must stay up: %+v err=%v", ms, err)
	}
}

func TestDisablePathExistsDoesNotStartOneshot(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(0)}
	hub := newFakePathHub()
	hub.exists[`C:\Data\ready.flag`] = true
	m := managerWithExists(t, launch, hub, map[string]string{
		"foo.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.path": `
[Path]
PathExists=C:\Data\ready.flag
[Install]
WantedBy=default.target
`,
	})
	if _, err := m.Enable("foo.path"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Disable("foo.path"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "foo.path", core.Inactive)
	// Synchronous startup found no satisfied condition and queued no activation.
	if launch.nstarts() != 0 {
		t.Fatal("disabled path unit must not start the oneshot")
	}
}

func TestVerifyPathExistsPairOverPipe(t *testing.T) {
	t.Parallel()
	hub := newFakePathHub()
	m := managerWithExists(t, &fakeLauncher{}, hub, pathExistsPair())
	client, stop := serveManager(t, m, protocol.AllowAdmin)
	defer stop()
	ver, err := client.Verify(context.Background(), "foo.path")
	if err != nil || !ver.OK {
		t.Fatalf("verify = %+v err=%v", ver, err)
	}
}

func TestPathExistsCloseStopsWatch(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(0)}
	hub := newFakePathHub()
	m := managerWithExists(t, launch, hub, pathExistsPair())
	if _, err := m.Start(context.Background(), "foo.path"); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "foo.path", core.Active)
	m.Close()
	hub.mu.Lock()
	w := hub.existsWatches[`C:\Data\ready.flag`]
	hub.mu.Unlock()
	if w == nil || !w.closed {
		t.Fatal("Close must close the PathExists watch")
	}
	if launch.nstarts() != 0 {
		t.Fatal("Close must not start the counterpart")
	}
}

func TestUserManagerAcceptsPathExists(t *testing.T) {
	t.Parallel()
	hub := newFakePathHub()
	launch := &scriptedLauncher{exitAll: intPtr(0)}
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, units, "foo.service", `
[Service]
Type=oneshot
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`)
	writeUnit(t, units, "foo.path", `
[Path]
PathExists=C:\Data\ready.flag
`)
	m, err := New(Config{
		BaseDir:        dir,
		Launch:         launch,
		PathOpen:       hub.Open,
		PathExistsOpen: hub.OpenExists,
		PathExists:     hub.Exists,
		UserScope:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	if _, err := m.Start(context.Background(), "foo.path"); err != nil {
		t.Fatal(err)
	}
	hub.SetExists(`C:\Data\ready.flag`, true)
	waitStarts(t, launch, 1, 2*time.Second)
}

func managerWithPath(t *testing.T, launch runtime.Launcher, open pathwatch.OpenFunc, files map[string]string) *Manager {
	t.Helper()
	return managerWithPathCfg(t, Config{Launch: launch, PathOpen: open}, files)
}

func managerWithExists(t *testing.T, launch runtime.Launcher, hub *fakePathHub, files map[string]string) *Manager {
	t.Helper()
	return managerWithPathCfg(t, Config{
		Launch:         launch,
		PathOpen:       hub.Open,
		PathExistsOpen: hub.OpenExists,
		PathExists:     hub.Exists,
	}, files)
}

func managerWithPathCfg(t *testing.T, cfg Config, files map[string]string) *Manager {
	t.Helper()
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		writeUnit(t, units, name, body)
	}
	cfg.BaseDir = dir
	m, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	return m
}

type fakePathHub struct {
	mu               sync.Mutex
	openErr          error
	existsOpenErr    error
	byKey            map[string]*fakePathWatch
	existsWatches    map[string]*fakePathWatch
	exists           map[string]bool
	acknowledgements map[string]chan struct{}
}

func newFakePathHub() *fakePathHub {
	return &fakePathHub{
		byKey:         make(map[string]*fakePathWatch),
		existsWatches: make(map[string]*fakePathWatch),
		exists:        make(map[string]bool),
	}
}

func (h *fakePathHub) Open(s pathwatch.Spec) (pathwatch.Watch, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.openErr != nil {
		return nil, h.openErr
	}
	w := &fakePathWatch{ch: make(chan struct{}, 8)}
	h.byKey[s.Raw] = w
	return w, nil
}

func (h *fakePathHub) OpenExists(s pathwatch.Spec) (pathwatch.Watch, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.existsOpenErr != nil {
		return nil, h.existsOpenErr
	}
	w := &fakePathWatch{ch: make(chan struct{}, 8)}
	h.existsWatches[s.Raw] = w
	if ack := h.acknowledgements[s.Raw]; ack != nil {
		return &acknowledgedWatch{watchIO: w, waiting: ack}, nil
	}
	return w, nil
}

func (h *fakePathHub) Exists(s pathwatch.Spec) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.exists[s.Raw], nil
}

func (h *fakePathHub) Fire(raw string) {
	h.mu.Lock()
	w := h.byKey[raw]
	h.mu.Unlock()
	if w != nil {
		w.ch <- struct{}{}
	}
}

func (h *fakePathHub) SetExists(raw string, v bool) {
	h.mu.Lock()
	h.exists[raw] = v
	w := h.existsWatches[raw]
	h.mu.Unlock()
	if w != nil {
		w.ch <- struct{}{}
	}
}

type fakePathWatch struct {
	mu     sync.Mutex
	ch     chan struct{}
	closed bool
}

func (w *fakePathWatch) C() <-chan struct{} { return w.ch }

func (w *fakePathWatch) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	close(w.ch)
	return nil
}
