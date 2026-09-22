package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/journal"
)

func TestNewReleasesStoresWhenDaemonLogOpenFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, journal.DaemonDirName), []byte("not-dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{BaseDir: dir, Launch: &fakeLauncher{}}); err == nil {
		t.Fatal("expected daemon log open failure")
	}
	if err := os.Remove(filepath.Join(dir, journal.DaemonDirName)); err != nil {
		t.Fatal(err)
	}
	m, err := New(Config{BaseDir: dir, Launch: &fakeLauncher{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
}

func TestDaemonLogStallDoesNotBlockLifecycle(t *testing.T) {
	dir := t.TempDir()
	release := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	m, err := New(Config{
		BaseDir: dir,
		Launch:  &fakeLauncher{},
		DaemonLogWrite: func([]byte) error {
			once.Do(func() { close(entered) })
			<-release
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(func() {
		unblock()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := m.CloseContext(ctx); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("daemon log did not reach the stalled sink")
	}
	if err := os.MkdirAll(m.cfg.UnitsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, m.cfg.UnitsDir(), "work.target", "[Unit]\nDescription=Owned\n")
	writeUnit(t, m.cfg.UnitsDir(), "other.target", "[Unit]\nDescription=Other\n")
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	statusDone := make(chan error, 1)
	go func() {
		st, err := m.Status("")
		if err != nil || st == nil || st.Machine == nil || len(st.Machine.DaemonEvents) == 0 || st.Machine.DaemonEvents[0].Code != journal.DaemonEventOpen {
			statusDone <- fmt.Errorf("status missed daemon events: %+v %v", st, err)
			return
		}
		statusDone <- nil
	}()
	select {
	case err := <-statusDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("machine status waited on the daemon log sink")
	}
	decision := make(chan struct{})
	go func() {
		m.mu.Lock()
		m.units["work.target"].step(core.EventStopFinished)
		m.mu.Unlock()
		close(decision)
	}()
	select {
	case <-decision:
	case <-time.After(time.Second):
		t.Fatal("rejected transition waited on the daemon log sink")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := m.Start(ctx, "other.target"); err != nil {
		t.Fatal(err)
	}
	snap, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "daemonEvents") || strings.Contains(string(raw), "daemonLogDropped") {
		t.Fatalf("snapshot included daemon-log observations: %s", raw)
	}
}

func TestStatusAndSnapshotExposeRestartBudget(t *testing.T) {
	launch := &scriptedLauncher{exitU32: uint32Ptr(7), holdAutoExit: true}
	body := "[Unit]\nFormatVersion=2\nStartLimitIntervalSec=60s\nStartLimitBurst=3\n[Service]\nExecStart=C:\\Tools\\worker.exe\nRestart=on-failure\nRestartSec=2s\nRestartBackoff=exponential\nRestartMaxDelaySec=8s\n"
	limit := "[Unit]\nStartLimitIntervalSec=60s\nStartLimitBurst=3\n[Service]\nExecStart=C:\\Tools\\worker.exe\nRestart=on-failure\nRestartSec=2s\n"
	m, clock := managerWithFake(t, launch, map[string]string{"work.service": body, "limit.service": limit})
	loaded, err := m.Status("work")
	if err != nil || loaded.Unit.RestartBudget == nil || loaded.Unit.RestartBudget.Policy != "on-failure" || loaded.Unit.RestartBudget.Burst != 3 || loaded.Unit.RestartBudget.IntervalSec != 60 || loaded.Unit.RestartBudget.MaxDelaySec != 8 || loaded.Unit.RestartBudget.StartsInWindow != 0 || loaded.Unit.RestartBudget.Remaining == nil || *loaded.Unit.RestartBudget.Remaining != 3 {
		t.Fatalf("loaded budget = %+v %v", loaded.Unit.RestartBudget, err)
	}
	crashToStartLimit(t, m, clock, launch, "limit.service", 3, 2*time.Second)
	status, err := m.Status("limit")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	view := snapshotUnit(t, snapshot, "limit.service")
	budget := status.Unit.RestartBudget
	if budget == nil || view.RestartBudget == nil || budget.Remaining == nil || view.RestartBudget.Remaining == nil {
		t.Fatalf("restart budget missing: status=%+v snapshot=%+v", status.Unit, view)
	}
	if budget.Policy != "on-failure" || budget.Burst != 3 || budget.IntervalSec != 60 || budget.StartsInWindow != 3 || *budget.Remaining != 0 {
		t.Fatalf("status budget = %+v", budget)
	}
	if view.RestartBudget.Policy != budget.Policy || view.RestartBudget.Burst != budget.Burst || view.RestartBudget.IntervalSec != budget.IntervalSec || view.RestartBudget.StartsInWindow != budget.StartsInWindow || *view.RestartBudget.Remaining != *budget.Remaining {
		t.Fatalf("snapshot budget = %+v status=%+v", view.RestartBudget, budget)
	}
	if view.LoadState != status.Unit.LoadState || view.ActiveState != "failed" || view.Health == "" || view.Health != status.Unit.Health || view.Error == "" || view.Error != status.Unit.Error || view.Reason != core.ReasonStartLimit || view.InvocationID == "" || view.InvocationID != status.Unit.InvocationID || view.ConfigRevision == "" || view.ConfigRevision != status.Unit.ConfigRevision || view.InvocationConfigRevision != status.Unit.InvocationConfigRevision || view.LastOperationID == "" || view.LastOperationID != status.Unit.LastOperationID {
		t.Fatalf("identity/lifecycle mismatch\nstatus=%+v\nsnapshot=%+v", status.Unit, view)
	}
	*view.RestartBudget.Remaining = 99
	again, err := m.Snapshot()
	if err != nil || snapshotUnit(t, again, "limit.service").RestartBudget == nil || *snapshotUnit(t, again, "limit.service").RestartBudget.Remaining != 0 {
		t.Fatal("published restart budget aliased manager state")
	}
}

func TestTimerStorageStaysOnStatusAndListTimers(t *testing.T) {
	m, clock := managerWithFake(t, &fakeLauncher{}, map[string]string{
		"work.timer":   "[Timer]\nOnStartupSec=1s\n",
		"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n",
	})
	if _, err := m.Start(context.Background(), "work.timer"); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Second)
	var storage, schedule string
	waitCond(t, func() bool {
		st, err := m.Status("work.timer")
		listed, listErr := m.ListTimers()
		if err != nil || listErr != nil || st.Unit == nil || len(listed.Timers) != 1 {
			return false
		}
		if st.Unit.TimerStorageState == "" || listed.Timers[0].StorageState != st.Unit.TimerStorageState || listed.Timers[0].ScheduleState != st.Unit.TimerScheduleState {
			return false
		}
		storage, schedule = st.Unit.TimerStorageState, st.Unit.TimerScheduleState
		return true
	})
	if storage == "" || schedule == "" {
		t.Fatalf("timer storage was not visible on status and list-timers: %s/%s", storage, schedule)
	}
	snapshot, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "timerStorage") || strings.Contains(string(raw), "storageState") || strings.Contains(string(raw), "scheduleState") {
		t.Fatalf("snapshot included timer storage: %s", raw)
	}
}

func uint32Ptr(n uint32) *uint32 { return &n }
