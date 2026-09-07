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
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/registry"
	"github.com/PLN/winunitd/internal/runtime"
)

func TestListUnitsShowsRegistry(t *testing.T) {
	t.Parallel()
	m := managerWithRegistry(t, &fakeLauncher{}, newFakeRegHub().Open, map[string]string{
		"foo.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.registry": `
[Registry]
RegistryChanged=HKLM\Software\Example
`,
	})
	list, err := m.ListUnits()
	if err != nil {
		t.Fatal(err)
	}
	var found protocol.UnitStatus
	for _, u := range list.Units {
		if u.Name == "foo.registry" {
			found = u
		}
	}
	if found.Name == "" || found.Kind != "registry" {
		t.Fatalf("list-units missing .registry: %+v", list.Units)
	}
}

func TestRegistryChangeStartsOneshotOnce(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(0)}
	hub := newFakeRegHub()
	m := managerWithRegistry(t, launch, hub.Open, map[string]string{
		"foo.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.registry": `
[Registry]
RegistryChanged=HKLM\Software\Example
`,
	})
	if _, err := m.Start(context.Background(), "foo.registry"); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "foo.registry", core.Active)
	if launch.nstarts() != 0 {
		t.Fatal("watching must not start the oneshot until a change")
	}
	hub.Fire(`HKLM\Software\Example`)
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
	hub.Fire(`HKLM\Software\Example`)
	waitStarts(t, launch, 2, 2*time.Second)
}

func TestRegistryChangeDoesNotRestartRunningSimple(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	hub := newFakeRegHub()
	waiting := make(chan struct{}, 8)
	m := managerWithRegistry(t, launch, func(spec registry.Key) (registry.Watch, error) {
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
		"foo.registry": `
[Registry]
RegistryChanged=HKLM\Software\Example
`,
	})
	if _, err := m.Start(context.Background(), "foo.service"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(context.Background(), "foo.registry"); err != nil {
		t.Fatal(err)
	}
	if n := len(launch.units()); n != 1 {
		t.Fatalf("starts = %d, want 1", n)
	}
	waitWatchCycle(t, waiting)
	hub.Fire(`HKLM\Software\Example`)
	waitWatchCycle(t, waiting)
	if n := len(launch.units()); n != 1 {
		t.Fatalf("running simple must not restart; starts = %d", n)
	}
}

func TestRegistryMissingKeyFailsConfiguration(t *testing.T) {
	t.Parallel()
	hub := newFakeRegHub()
	hub.openErr = registry.ErrMissingKey
	m := managerWithRegistry(t, &fakeLauncher{}, hub.Open, map[string]string{
		"foo.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.registry": `
[Registry]
RegistryChanged=HKLM\Software\Example
`,
	})
	_, err := m.Start(context.Background(), "foo.registry")
	if err == nil {
		t.Fatal("missing key must fail start")
	}
	assertState(t, m, "foo.registry", core.Failed)
	st, err := m.Status("foo.registry")
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

func TestRegistryDeletedKeyFailsConfiguration(t *testing.T) {
	t.Parallel()
	hub := newFakeRegHub()
	m := managerWithRegistry(t, &fakeLauncher{}, hub.Open, map[string]string{
		"foo.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.registry": `
[Registry]
RegistryChanged=HKLM\Software\Example
`,
	})
	if _, err := m.Start(context.Background(), "foo.registry"); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "foo.registry", core.Active)
	hub.CloseKey(`HKLM\Software\Example`)
	waitUntil(t, 2*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.stateOfLocked("foo.registry") == core.Failed
	})
	st, err := m.Status("foo.registry")
	if err != nil || st.Unit == nil || st.Unit.Reason != core.ReasonConfiguration {
		t.Fatalf("status = %+v err=%v", st, err)
	}
}

func TestSystemVerifyRejectsHKCU(t *testing.T) {
	t.Parallel()
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
	writeUnit(t, units, "foo.registry", `
[Registry]
RegistryChanged=HKCU\Software\Example
`)
	m, err := New(Config{BaseDir: dir, Launch: &fakeLauncher{}, RegistryOpen: newFakeRegHub().Open})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	rel, err := m.Reload()
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(rel.Errors, "\n")
	if !strings.Contains(joined, "HKCU") {
		t.Fatalf("reload errors = %s", joined)
	}
	m.mu.Lock()
	_, ok := m.units["foo.registry"]
	m.mu.Unlock()
	if ok {
		t.Fatal("system manager must not load HKCU registry units")
	}
}

func TestUserManagerAcceptsHKCU(t *testing.T) {
	t.Parallel()
	hub := newFakeRegHub()
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
	writeUnit(t, units, "foo.registry", `
[Registry]
RegistryChanged=HKCU\Software\Example
`)
	m, err := New(Config{BaseDir: dir, Launch: launch, RegistryOpen: hub.Open, UserScope: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	if _, err := m.Start(context.Background(), "foo.registry"); err != nil {
		t.Fatal(err)
	}
	hub.Fire(`HKCU\Software\Example`)
	waitStarts(t, launch, 1, 2*time.Second)
}

func TestDisableRegistryDoesNotStartOneshot(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(0)}
	hub := newFakeRegHub()
	m := managerWithRegistry(t, launch, hub.Open, map[string]string{
		"foo.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.registry": `
[Registry]
RegistryChanged=HKLM\Software\Example
[Install]
WantedBy=default.target
`,
	})
	if _, err := m.Enable("foo.registry"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Disable("foo.registry"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "foo.registry", core.Inactive)
	hub.Fire(`HKLM\Software\Example`)
	// Boot completed synchronously; the disabled watch was never armed.
	if launch.nstarts() != 0 {
		t.Fatal("disabled registry unit must not start the oneshot")
	}
}

func TestVerifyRegistryPairOverPipe(t *testing.T) {
	t.Parallel()
	m := managerWithRegistry(t, &fakeLauncher{}, newFakeRegHub().Open, map[string]string{
		"foo.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.registry": `
[Registry]
RegistryChanged=HKLM\Software\Example
`,
	})
	client, stop := serveManager(t, m, protocol.AllowAdmin)
	defer stop()
	ver, err := client.Verify(context.Background(), "foo.registry")
	if err != nil || !ver.OK {
		t.Fatalf("verify = %+v err=%v", ver, err)
	}
}

func managerWithRegistry(t *testing.T, launch runtime.Launcher, open registry.OpenFunc, files map[string]string) *Manager {
	t.Helper()
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		writeUnit(t, units, name, body)
	}
	m, err := New(Config{BaseDir: dir, Launch: launch, RegistryOpen: open})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	return m
}

type fakeRegHub struct {
	mu      sync.Mutex
	openErr error
	byKey   map[string]*fakeRegWatch
}

func newFakeRegHub() *fakeRegHub {
	return &fakeRegHub{byKey: make(map[string]*fakeRegWatch)}
}

func (h *fakeRegHub) Open(k registry.Key) (registry.Watch, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.openErr != nil {
		return nil, h.openErr
	}
	w := &fakeRegWatch{ch: make(chan struct{}, 8)}
	h.byKey[k.Raw] = w
	return w, nil
}

func (h *fakeRegHub) Fire(raw string) {
	h.mu.Lock()
	w := h.byKey[raw]
	h.mu.Unlock()
	if w != nil {
		w.ch <- struct{}{}
	}
}

func (h *fakeRegHub) CloseKey(raw string) {
	h.mu.Lock()
	w := h.byKey[raw]
	h.mu.Unlock()
	if w != nil {
		_ = w.Close()
	}
}

type fakeRegWatch struct {
	mu     sync.Mutex
	ch     chan struct{}
	closed bool
}

func (w *fakeRegWatch) C() <-chan struct{} { return w.ch }

func (w *fakeRegWatch) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	close(w.ch)
	return nil
}
