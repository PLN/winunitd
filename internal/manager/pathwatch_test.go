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
	time.Sleep(200 * time.Millisecond)
	if launch.nstarts() != 0 {
		t.Fatal("unrelated path must not start the oneshot")
	}
}

func TestPathDoesNotRestartRunningSimple(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	hub := newFakePathHub()
	m := managerWithPath(t, launch, hub.Open, map[string]string{
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
	hub.Fire(`C:\Data\incoming`)
	time.Sleep(200 * time.Millisecond)
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
	time.Sleep(200 * time.Millisecond)
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

func managerWithPath(t *testing.T, launch runtime.Launcher, open pathwatch.OpenFunc, files map[string]string) *Manager {
	t.Helper()
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		writeUnit(t, units, name, body)
	}
	m, err := New(Config{BaseDir: dir, Launch: launch, PathOpen: open})
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
	mu      sync.Mutex
	openErr error
	byKey   map[string]*fakePathWatch
}

func newFakePathHub() *fakePathHub {
	return &fakePathHub{byKey: make(map[string]*fakePathWatch)}
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

func (h *fakePathHub) Fire(raw string) {
	h.mu.Lock()
	w := h.byKey[raw]
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

