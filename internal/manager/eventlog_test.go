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
	"github.com/PLN/winunitd/internal/eventlog"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
)

func TestListUnitsShowsEventLog(t *testing.T) {
	t.Parallel()
	m := managerWithEventLog(t, &fakeLauncher{}, newFakeEvtHub().Open, map[string]string{
		"foo.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.eventlog": `
[EventLog]
EventLogTrigger=Application:EventID=1234
`,
	})
	list, err := m.ListUnits()
	if err != nil {
		t.Fatal(err)
	}
	var found protocol.UnitStatus
	for _, u := range list.Units {
		if u.Name == "foo.eventlog" {
			found = u
		}
	}
	if found.Name == "" || found.Kind != "eventlog" {
		t.Fatalf("list-units missing .eventlog: %+v", list.Units)
	}
}

func TestEventLogMatchStartsOneshotOnce(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(0)}
	hub := newFakeEvtHub()
	m := managerWithEventLog(t, launch, hub.Open, map[string]string{
		"foo.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.eventlog": `
[EventLog]
EventLogTrigger=Application:EventID=1234
`,
	})
	if _, err := m.Start(context.Background(), "foo.eventlog"); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "foo.eventlog", core.Active)
	if launch.nstarts() != 0 {
		t.Fatal("watching must not start the oneshot until a match")
	}
	hub.Fire("Application:EventID=1234")
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
	hub.Fire("Application:EventID=1234")
	waitStarts(t, launch, 2, 2*time.Second)
}

func TestEventLogUnrelatedEventIDDoesNotStart(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(0)}
	hub := newFakeEvtHub()
	m := managerWithEventLog(t, launch, hub.Open, map[string]string{
		"foo.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.eventlog": `
[EventLog]
EventLogTrigger=Application:EventID=1234
`,
	})
	if _, err := m.Start(context.Background(), "foo.eventlog"); err != nil {
		t.Fatal(err)
	}
	hub.Fire("Application:EventID=9999")
	// Fake dispatch returns synchronously without sending to an unrelated watch.
	if launch.nstarts() != 0 {
		t.Fatal("unrelated EventID must not start the oneshot")
	}
}

func TestEventLogDoesNotRestartRunningSimple(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	hub := newFakeEvtHub()
	waiting := make(chan struct{}, 8)
	m := managerWithEventLog(t, launch, func(spec eventlog.Trigger) (eventlog.Subscription, error) {
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
		"foo.eventlog": `
[EventLog]
EventLogTrigger=Application:EventID=1234
`,
	})
	if _, err := m.Start(context.Background(), "foo.service"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(context.Background(), "foo.eventlog"); err != nil {
		t.Fatal(err)
	}
	if n := len(launch.units()); n != 1 {
		t.Fatalf("starts = %d, want 1", n)
	}
	gen := genOf(t, m, "foo.service")
	waitWatchCycle(t, waiting) // Initial receive is armed.
	hub.Fire("Application:EventID=1234")
	waitWatchCycle(t, waiting) // Callback returned and the next receive is armed.
	if n := len(launch.units()); n != 1 {
		t.Fatalf("running simple must not restart; starts = %d", n)
	}
	if got := genOf(t, m, "foo.service"); got != gen {
		t.Fatalf("gen = %d after matching event, want %d (C1 must not bump)", got, gen)
	}
}

func TestEventLogUnknownChannelFailsConfiguration(t *testing.T) {
	t.Parallel()
	hub := newFakeEvtHub()
	hub.openErr = eventlog.ErrUnknownChannel
	m := managerWithEventLog(t, &fakeLauncher{}, hub.Open, map[string]string{
		"foo.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.eventlog": `
[EventLog]
EventLogTrigger=NoSuchChannel:EventID=1
`,
	})
	_, err := m.Start(context.Background(), "foo.eventlog")
	if err == nil {
		t.Fatal("unknown channel must fail start")
	}
	assertState(t, m, "foo.eventlog", core.Failed)
	st, err := m.Status("foo.eventlog")
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

func TestUserVerifyRejectsSystemEventLog(t *testing.T) {
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
	writeUnit(t, units, "foo.eventlog", `
[EventLog]
EventLogTrigger=System:EventID=1
`)
	m, err := New(Config{BaseDir: dir, Launch: &fakeLauncher{}, EventLogOpen: newFakeEvtHub().Open, UserScope: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	rel, err := m.Reload()
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(rel.Errors, "\n")
	if !strings.Contains(joined, "System") {
		t.Fatalf("reload errors = %s", joined)
	}
	m.mu.Lock()
	_, ok := m.units["foo.eventlog"]
	m.mu.Unlock()
	if ok {
		t.Fatal("user manager must not load System eventlog units")
	}
}

func TestUserManagerAcceptsApplicationEventLog(t *testing.T) {
	t.Parallel()
	hub := newFakeEvtHub()
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
	writeUnit(t, units, "foo.eventlog", `
[EventLog]
EventLogTrigger=Application:EventID=1234
`)
	m, err := New(Config{BaseDir: dir, Launch: launch, EventLogOpen: hub.Open, UserScope: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	if _, err := m.Start(context.Background(), "foo.eventlog"); err != nil {
		t.Fatal(err)
	}
	hub.Fire("Application:EventID=1234")
	waitStarts(t, launch, 1, 2*time.Second)
}

func TestDisableEventLogDoesNotStartOneshot(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(0)}
	hub := newFakeEvtHub()
	m := managerWithEventLog(t, launch, hub.Open, map[string]string{
		"foo.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.eventlog": `
[EventLog]
EventLogTrigger=Application:EventID=1234
[Install]
WantedBy=default.target
`,
	})
	if _, err := m.Enable("foo.eventlog"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Disable("foo.eventlog"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "foo.eventlog", core.Inactive)
	hub.Fire("Application:EventID=1234")
	// Boot completed synchronously; the disabled watch was never armed.
	if launch.nstarts() != 0 {
		t.Fatal("disabled eventlog unit must not start the oneshot")
	}
}

func TestVerifyEventLogPairOverPipe(t *testing.T) {
	t.Parallel()
	m := managerWithEventLog(t, &fakeLauncher{}, newFakeEvtHub().Open, map[string]string{
		"foo.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.eventlog": `
[EventLog]
EventLogTrigger=Application:EventID=1234
`,
	})
	client, stop := serveManager(t, m, protocol.AllowAdmin)
	defer stop()
	ver, err := client.Verify(context.Background(), "foo.eventlog")
	if err != nil || !ver.OK {
		t.Fatalf("verify = %+v err=%v", ver, err)
	}
}

func TestEventLogCloseStopsWatch(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(0)}
	hub := newFakeEvtHub()
	m := managerWithEventLog(t, launch, hub.Open, map[string]string{
		"foo.service": `
[Service]
Type=oneshot
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
		"foo.eventlog": `
[EventLog]
EventLogTrigger=Application:EventID=1234
`,
	})
	if _, err := m.Start(context.Background(), "foo.eventlog"); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "foo.eventlog", core.Active)
	m.Close()
	hub.mu.Lock()
	s := hub.byKey["Application:EventID=1234"]
	hub.mu.Unlock()
	if s == nil || !s.closed {
		t.Fatal("Close must close the Event Log subscription")
	}
	if launch.nstarts() != 0 {
		t.Fatal("Close must not start the counterpart")
	}
}

func managerWithEventLog(t *testing.T, launch runtime.Launcher, open eventlog.OpenFunc, files map[string]string) *Manager {
	t.Helper()
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		writeUnit(t, units, name, body)
	}
	m, err := New(Config{BaseDir: dir, Launch: launch, EventLogOpen: open})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	return m
}

type fakeEvtHub struct {
	mu      sync.Mutex
	openErr error
	byKey   map[string]*fakeEvtSub
}

func newFakeEvtHub() *fakeEvtHub {
	return &fakeEvtHub{byKey: make(map[string]*fakeEvtSub)}
}

func (h *fakeEvtHub) Open(tr eventlog.Trigger) (eventlog.Subscription, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.openErr != nil {
		return nil, h.openErr
	}
	s := &fakeEvtSub{ch: make(chan struct{}, 8)}
	h.byKey[tr.Raw] = s
	return s, nil
}

func (h *fakeEvtHub) Fire(raw string) {
	h.mu.Lock()
	s := h.byKey[raw]
	h.mu.Unlock()
	if s != nil {
		s.ch <- struct{}{}
	}
}

type fakeEvtSub struct {
	mu     sync.Mutex
	ch     chan struct{}
	closed bool
}

func (s *fakeEvtSub) C() <-chan struct{} { return s.ch }

func (s *fakeEvtSub) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	close(s.ch)
	return nil
}
