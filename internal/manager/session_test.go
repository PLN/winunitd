package manager

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/runtime"
)

func userTestManager(t *testing.T, has func() bool, files map[string]string) (*Manager, *fakeLauncher) {
	t.Helper()
	launch := &fakeLauncher{}
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		writeUnit(t, units, name, body)
	}
	m, err := New(Config{
		BaseDir:               dir,
		Launch:                launch,
		UserScope:             true,
		HasInteractiveSession: has,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	return m, launch
}

func TestUserManagerLoadsGraphicalSessionTarget(t *testing.T) {
	t.Parallel()
	m, _ := userTestManager(t, func() bool { return false }, nil)
	st, err := m.Status(GraphicalSessionTarget)
	if err != nil {
		t.Fatal(err)
	}
	if st.Unit == nil || st.Unit.Kind != "target" || st.Unit.Path != "builtin:"+GraphicalSessionTarget {
		t.Fatalf("status = %+v", st.Unit)
	}
	if st.Unit.ActiveState != core.Inactive.String() {
		t.Fatalf("linger-without-session state = %s", st.Unit.ActiveState)
	}
	list, err := m.ListUnits()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, u := range list.Units {
		if u.Name == GraphicalSessionTarget {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("list-units must show graphical-session.target")
	}
}

func TestGraphicalSessionActiveAfterLogon(t *testing.T) {
	t.Parallel()
	var has atomic.Bool
	m, _ := userTestManager(t, has.Load, nil)
	if _, err := m.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.SyncGraphicalSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, GraphicalSessionTarget, core.Inactive)

	has.Store(true)
	if err := m.SyncGraphicalSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, GraphicalSessionTarget, core.Active)
}

func TestGraphicalSessionInactiveAfterLastLogoff(t *testing.T) {
	t.Parallel()
	var has atomic.Bool
	has.Store(true)
	m, _ := userTestManager(t, has.Load, nil)
	if err := m.SyncGraphicalSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, GraphicalSessionTarget, core.Active)

	has.Store(false)
	if err := m.SyncGraphicalSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, GraphicalSessionTarget, core.Inactive)
}

func TestGraphicalSessionLingerWithoutSessionStaysInactive(t *testing.T) {
	t.Parallel()
	m, launch := userTestManager(t, func() bool { return false }, map[string]string{
		"linger.service": `
[Service]
ExecStart=C:\Tools\linger.exe
WorkingDirectory=C:\Tools
[Install]
WantedBy=default.target
`,
	})
	if _, err := m.Enable("linger.service"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.SyncGraphicalSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, GraphicalSessionTarget, core.Inactive)
	assertState(t, m, DefaultTarget, core.Active)
	assertState(t, m, "linger.service", core.Active)
	if got := launch.units(); len(got) != 1 || got[0] != "linger.service" {
		t.Fatalf("started %v, want only linger.service", got)
	}
}

func TestRequiresInteractiveSessionSkipsWhenGraphicalSessionDown(t *testing.T) {
	t.Parallel()
	m, launch := userTestManager(t, func() bool { return false }, map[string]string{
		"gui.service": `
[Unit]
RequiresInteractiveSession=yes
[Service]
Type=oneshot
ExecStart=C:\Tools\gui.exe
WorkingDirectory=C:\Tools
`,
	})
	if err := m.SyncGraphicalSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, GraphicalSessionTarget, core.Inactive)
	res, err := m.Start(context.Background(), "gui.service")
	if err != nil {
		t.Fatalf("skip should not fail: %v", err)
	}
	if res.ActiveState != core.Inactive.String() {
		t.Fatalf("state = %s", res.ActiveState)
	}
	if len(launch.specs()) != 0 {
		t.Fatal("RequiresInteractiveSession units must skip when the target is down")
	}
}

func TestGraphicalSessionStopStopsWantedByInReverse(t *testing.T) {
	t.Parallel()
	var has atomic.Bool
	has.Store(true)
	m, launch := userTestManager(t, has.Load, map[string]string{
		"web.service": `
[Unit]
After=db.service
PartOf=graphical-session.target
[Service]
ExecStart=C:\Tools\web.exe
WorkingDirectory=C:\Tools
[Install]
WantedBy=graphical-session.target
`,
		"db.service": `
[Unit]
PartOf=graphical-session.target
[Service]
ExecStart=C:\Tools\db.exe
WorkingDirectory=C:\Tools
[Install]
WantedBy=graphical-session.target
`,
	})
	if _, err := m.Enable("web.service"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Enable("db.service"); err != nil {
		t.Fatal(err)
	}
	if err := m.SyncGraphicalSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, GraphicalSessionTarget, core.Active)
	assertState(t, m, "web.service", core.Active)
	assertState(t, m, "db.service", core.Active)

	has.Store(false)
	if err := m.SyncGraphicalSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, GraphicalSessionTarget, core.Inactive)
	assertState(t, m, "web.service", core.Inactive)
	assertState(t, m, "db.service", core.Inactive)
	got := launch.stopped()
	if len(got) < 2 || got[0] != "web.service" || got[1] != "db.service" {
		t.Fatalf("stop order = %v, want web then db", got)
	}
}

func TestLingerUnitsKeepRunningWhenGraphicalSessionStops(t *testing.T) {
	t.Parallel()
	var has atomic.Bool
	m, launch := userTestManager(t, has.Load, map[string]string{
		"linger.service": `
[Service]
ExecStart=C:\Tools\linger.exe
WorkingDirectory=C:\Tools
[Install]
WantedBy=default.target
`,
		"gui.service": `
[Unit]
RequiresInteractiveSession=yes
PartOf=graphical-session.target
[Service]
ExecStart=C:\Tools\gui.exe
WorkingDirectory=C:\Tools
[Install]
WantedBy=graphical-session.target
`,
	})
	if _, err := m.Enable("linger.service"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Enable("gui.service"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.SyncGraphicalSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, GraphicalSessionTarget, core.Inactive)
	assertState(t, m, "linger.service", core.Active)
	assertState(t, m, "gui.service", core.Inactive)
	if len(launch.specs()) != 1 {
		t.Fatalf("headless started %v", launch.units())
	}

	has.Store(true)
	if err := m.SyncGraphicalSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, GraphicalSessionTarget, core.Active)
	assertState(t, m, "gui.service", core.Active)
	assertState(t, m, "linger.service", core.Active)

	has.Store(false)
	if err := m.SyncGraphicalSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, GraphicalSessionTarget, core.Inactive)
	assertState(t, m, "gui.service", core.Inactive)
	assertState(t, m, "linger.service", core.Active)
}

func TestGraphicalSessionWatchInjectedCallbacks(t *testing.T) {
	t.Parallel()
	var has atomic.Bool
	m, _ := userTestManager(t, has.Load, nil)
	ch := make(chan runtime.SessionChange, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		m.WatchGraphicalSession(ctx, ch)
		close(done)
	}()

	if _, err := m.Boot(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.SyncGraphicalSession(ctx); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, GraphicalSessionTarget, core.Inactive)

	has.Store(true)
	ch <- runtime.SessionChange{SessionID: 3, Logon: true}
	waitUntil(t, 2*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.stateOfLocked(GraphicalSessionTarget) == core.Active
	})

	has.Store(false)
	ch <- runtime.SessionChange{SessionID: 3, Logon: false}
	waitUntil(t, 2*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.stateOfLocked(GraphicalSessionTarget) == core.Inactive
	})

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WatchGraphicalSession did not return")
	}
}

func TestSystemManagerSyncGraphicalSessionIsNoop(t *testing.T) {
	t.Parallel()
	m := testManager(t, nil)
	if err := m.SyncGraphicalSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Status(GraphicalSessionTarget); err == nil {
		t.Fatal("system manager must not load graphical-session.target")
	}
}
