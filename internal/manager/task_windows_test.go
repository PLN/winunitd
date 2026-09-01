//go:build windows

package manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/runtime"
)

func TestWindowsScheduledTaskProxyOrchestration(t *testing.T) {
	taskName := installManagerThrowawayTask(t)
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	writeUnit(t, units, "legacy-backup.service", fmt.Sprintf(`
[Service]
Type=scheduled-task
TaskName=%s
TimeoutStartSec=15s
TimeoutStopSec=15s
`, taskName))
	writeUnit(t, units, "app.service", fmt.Sprintf(`
[Unit]
Requires=legacy-backup.service
After=legacy-backup.service
[Service]
Type=simple
ExecStart=%s
ExecStartArg=%ssleep
WorkingDirectory=C:\
`, exe, winunitdHelperArgPrefix))

	m, err := New(Config{BaseDir: dir, Tasks: runtime.DefaultTaskScheduler()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}

	st, err := m.Start(context.Background(), "app")
	if err != nil {
		t.Fatal(err)
	}
	if st.ActiveState != "active" {
		t.Fatalf("app start = %+v", st)
	}

	proxy, err := m.Status("legacy-backup")
	if err != nil {
		t.Fatal(err)
	}
	if proxy.Unit == nil || proxy.Unit.ActiveState != "active" {
		t.Fatalf("proxy status after After= start = %+v", proxy.Unit)
	}
	if proxy.Unit.InvocationID != "" {
		t.Fatalf("Type=scheduled-task must omit InvocationID: %+v", proxy.Unit)
	}

	live, err := runtime.DefaultTaskScheduler().Query(taskName)
	if err != nil {
		t.Fatal(err)
	}
	if live.ActiveState() != "active" {
		t.Fatalf("task after After= wait = %+v (simple unit must wait until running)", live)
	}

	again, err := m.Start(context.Background(), "legacy-backup")
	if err != nil {
		t.Fatal(err)
	}
	if again.ActiveState != "active" {
		t.Fatalf("already-running start = %+v", again)
	}

	stopped, err := m.Stop("legacy-backup")
	if err != nil {
		t.Fatal(err)
	}
	if stopped.ActiveState != "inactive" {
		t.Fatalf("stop = %+v", stopped)
	}
	afterStop, err := m.Status("legacy-backup")
	if err != nil {
		t.Fatal(err)
	}
	if afterStop.Unit.ActiveState != "inactive" {
		t.Fatalf("status after stop = %+v", afterStop.Unit)
	}

	againStop, err := m.Stop("legacy-backup")
	if err != nil {
		t.Fatal(err)
	}
	if againStop.ActiveState != "inactive" {
		t.Fatalf("already-stopped = %+v", againStop)
	}
}

func TestWindowsScheduledTaskMissingTaskStartFails(t *testing.T) {
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, units, "missing.service", `
[Service]
Type=scheduled-task
TaskName=\WinUnitdTS1\no-such-task-ts1
Restart=no
`)
	m, err := New(Config{BaseDir: dir, Tasks: runtime.DefaultTaskScheduler()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(context.Background(), "missing"); err == nil {
		t.Fatal("missing task must fail start")
	}
}

func installManagerThrowawayTask(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	folder := "WinUnitdTS1"
	name := fmt.Sprintf("wu-mgr-%d-%d", os.Getpid(), time.Now().UnixNano()%1e9)
	full, err := runtime.RegisterThrowawayTask(folder, name, exe, winunitdHelperArgPrefix+"sleep")
	if err != nil {
		t.Skipf("Task Scheduler register %s: %v", full, err)
	}
	t.Cleanup(func() {
		ts := runtime.DefaultTaskScheduler()
		_, _ = ts.Stop(context.Background(), full, 10*time.Second)
		if err := runtime.DeleteThrowawayTask(folder, name); err != nil {
			t.Errorf("delete throwaway task %s: %v", full, err)
		}
	})
	return full
}
