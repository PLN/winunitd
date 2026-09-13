//go:build windows

package manager

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/runtime"
)

func nativeDependencyWorker(t *testing.T) string {
	t.Helper()
	argv, err := json.Marshal(windowsStayAliveArgv(t))
	if err != nil {
		t.Fatal(err)
	}
	return "[Service]\nExecStart=" + string(argv) + "\nTimeoutStopSec=5s\n"
}

func nativeDependencyProcess(t *testing.T, m *Manager, name string) runtime.Process {
	t.Helper()
	m.mu.Lock()
	proc := m.units[name].proc
	m.mu.Unlock()
	if proc == nil || !proc.Alive() {
		t.Fatalf("%s has no live owned process", name)
	}
	return proc
}

func TestWindowsPartOfRestartAndCapturedStop(t *testing.T) {
	worker := nativeDependencyWorker(t)
	m := managerWith(t, runtime.NewLauncher(nil), map[string]string{
		"app.target":   "[Unit]\nDescription=Dependency fixture\n",
		"db.service":   "[Unit]\nPartOf=app.target\n" + worker,
		"web.service":  "[Unit]\nPartOf=app.target\nAfter=db.service\n" + worker,
		"idle.service": "[Unit]\nPartOf=app.target\n" + worker,
	})
	for _, name := range []string{"app.target", "db.service", "web.service"} {
		if _, err := m.Start(context.Background(), name); err != nil {
			t.Fatal(err)
		}
	}
	for cycle := 0; cycle < 3; cycle++ {
		db, web := nativeDependencyProcess(t, m, "db.service"), nativeDependencyProcess(t, m, "web.service")
		if _, err := m.Restart(context.Background(), "app.target"); err != nil {
			t.Fatal(err)
		}
		if db.Alive() || web.Alive() {
			t.Fatal("PartOf replacement preceded old process cleanup")
		}
		nextDB, nextWeb := nativeDependencyProcess(t, m, "db.service"), nativeDependencyProcess(t, m, "web.service")
		if db == nextDB || web == nextWeb {
			t.Fatal("restart reused old invocation")
		}
		assertState(t, m, "idle.service", core.Inactive)
	}
	db, web := nativeDependencyProcess(t, m, "db.service"), nativeDependencyProcess(t, m, "web.service")
	for _, name := range []string{"db.service", "web.service"} {
		writeUnit(t, m.cfg.UnitsDir(), name, worker)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stop("app.target"); err != nil {
		t.Fatal(err)
	}
	if db.Alive() || web.Alive() {
		t.Fatal("reload lost captured PartOf stop participation")
	}
	for _, name := range []string{"db.service", "web.service"} {
		assertState(t, m, name, core.Inactive)
		if _, err := m.Start(context.Background(), name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Start(context.Background(), "app.target"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stop("app.target"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"db.service", "web.service"} {
		nativeDependencyProcess(t, m, name)
		if _, err := m.Stop(name); err != nil {
			t.Fatal(err)
		}
		status, err := m.Status(name)
		if err != nil || status.Unit.MainPID != 0 || len(status.Unit.PendingCleanup) != 0 {
			t.Fatal("final dependency cleanup incomplete")
		}
	}
}

func TestWindowsBoundPeerExitPreservesRequiresAndPartOf(t *testing.T) {
	worker := nativeDependencyWorker(t)
	m := managerWith(t, runtime.NewLauncher(nil), map[string]string{
		"peer.service":     worker + "Restart=always\nRestartSec=100ms\n",
		"bound.service":    "[Unit]\nBindsTo=peer.service\nAfter=peer.service\n" + worker + "Restart=always\n",
		"required.service": "[Unit]\nRequires=peer.service\nAfter=peer.service\n" + worker,
		"member.service":   "[Unit]\nPartOf=peer.service\n" + worker,
	})
	for _, name := range []string{"bound.service", "required.service", "member.service"} {
		if _, err := m.Start(context.Background(), name); err != nil {
			t.Fatal(err)
		}
	}
	peer, bound := nativeDependencyProcess(t, m, "peer.service"), nativeDependencyProcess(t, m, "bound.service")
	required, member := nativeDependencyProcess(t, m, "required.service"), nativeDependencyProcess(t, m, "member.service")
	if err := peer.Job().Kill(); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 5*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.units["bound.service"].state == core.Inactive && m.units["bound.service"].proc == nil && m.boundStopsDone == nil && m.units["peer.service"].state == core.Active && m.units["peer.service"].proc != peer
	})
	if peer.Alive() || bound.Alive() || !required.Alive() || !member.Alive() {
		t.Fatal("unexpected-exit dependency policy violated")
	}
	if nativeDependencyProcess(t, m, "required.service") != required || nativeDependencyProcess(t, m, "member.service") != member {
		t.Fatal("unaffected dependency was replaced")
	}
	if _, err := m.Stop("peer.service"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"peer.service", "bound.service", "required.service", "member.service"} {
		status, err := m.Status(name)
		if err != nil || status.Unit.MainPID != 0 || len(status.Unit.PendingCleanup) != 0 {
			t.Fatalf("%s retained native cleanup", name)
		}
	}
}
