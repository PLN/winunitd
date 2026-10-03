package manager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/journal"
	"github.com/PLN/winunitd/internal/protocol"
)

// Servicing stops the manager and starts another on the same data. Only
// enabled units return. Journal and daemon-log records keep the invocation
// and operation identities they were written with, while the previous
// manager's live operation history is gone: its operation IDs are unknown.
func TestServicingRestartRestoresEnabledUnitsAndKeepsRecords(t *testing.T) {
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, units, "enabled.service", "[Service]\nExecStart=C:\\Tools\\enabled.exe\n[Install]\nWantedBy=default.target\n")
	writeUnit(t, units, "manual.service", "[Service]\nExecStart=C:\\Tools\\manual.exe\n")
	launch := &fakeLauncher{stdout: "before servicing\n"}
	open := func() *Manager {
		t.Helper()
		m, err := New(Config{BaseDir: dir, Launch: launch})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.Reload(); err != nil {
			t.Fatal(err)
		}
		return m
	}
	ctx := context.Background()

	before := open()
	if _, err := before.Enable("enabled.service"); err != nil {
		t.Fatal(err)
	}
	if _, err := before.Boot(ctx); err != nil {
		t.Fatal(err)
	}
	manual, err := before.Start(ctx, "manual.service")
	if err != nil || manual.OperationID == "" {
		t.Fatalf("manual start %+v %v", manual, err)
	}
	if _, err := before.Operation(manual.OperationID); err != nil {
		t.Fatalf("operation before servicing: %v", err)
	}
	oldEnabled := mustInvocationID(t, before, "enabled.service")
	oldManual := mustInvocationID(t, before, "manual.service")
	waitJournalMessage(t, before, "enabled.service", "before servicing")
	waitJournalMessage(t, before, "manual.service", "before servicing")
	st, err := before.Status("")
	if err != nil {
		t.Fatal(err)
	}
	oldEvents := st.Machine.DaemonEvents
	stopAll(before)

	launch.mu.Lock()
	launch.stdout = "after servicing\n"
	launch.mu.Unlock()
	after := open()
	t.Cleanup(func() { stopAll(after) })
	if _, err := after.Boot(ctx); err != nil {
		t.Fatal(err)
	}
	assertState(t, after, "enabled.service", core.Active)
	assertState(t, after, "manual.service", core.Inactive)
	newEnabled := mustInvocationID(t, after, "enabled.service")
	if newEnabled == oldEnabled {
		t.Fatal("the restored unit reused its old invocation ID")
	}

	var pe *protocol.Error
	if _, err := after.Operation(manual.OperationID); !errors.As(err, &pe) || pe.Code != protocol.CodeNotFound {
		t.Fatalf("old operation after servicing: %v", err)
	}

	waitJournalMessage(t, after, "enabled.service", "after servicing")
	logs, err := after.Logs(protocol.LogsParams{Unit: "enabled.service"})
	if err != nil {
		t.Fatal(err)
	}
	assertLogInvocation(t, logs, "before servicing", oldEnabled)
	assertLogInvocation(t, logs, "after servicing", newEnabled)
	logs, err = after.Logs(protocol.LogsParams{Unit: "manual.service"})
	if err != nil {
		t.Fatal(err)
	}
	assertLogInvocation(t, logs, "before servicing", oldManual)

	assertDaemonLogKept(t, after, oldEvents)
}

// The daemon log keeps what the previous manager recorded, including the
// invocation and operation IDs of a start-limit record.
func TestServicingRestartKeepsDaemonLogRecords(t *testing.T) {
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, units, "limited.service", "[Unit]\nStartLimitIntervalSec=1h\nStartLimitBurst=1\n[Service]\nExecStart=C:\\Tools\\limited.exe\nRestart=on-failure\n")
	before, err := New(Config{BaseDir: dir, Launch: &scriptedLauncher{exitAll: intPtr(2)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := before.Reload(); err != nil {
		t.Fatal(err)
	}
	op, err := before.Start(context.Background(), "limited.service")
	if err != nil {
		t.Fatal(err)
	}
	waitCond(t, func() bool {
		st, err := before.Status("limited.service")
		return err == nil && st.Unit != nil && st.Unit.Reason == core.ReasonStartLimit
	})
	invocation := mustInvocationID(t, before, "limited.service")
	st, err := before.Status("")
	if err != nil {
		t.Fatal(err)
	}
	oldEvents := st.Machine.DaemonEvents
	if !slices.ContainsFunc(oldEvents, func(ev protocol.DaemonEvent) bool {
		return ev.Code == journal.DaemonEventStartLimit && ev.Unit == "limited.service" && ev.InvocationID == invocation && ev.OperationID == op.OperationID
	}) {
		t.Fatalf("no start-limit record with the invocation and operation: %+v", oldEvents)
	}
	stopAll(before)
	after, err := New(Config{BaseDir: dir, Launch: &fakeLauncher{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(after) })
	assertDaemonLogKept(t, after, oldEvents)
}

// assertDaemonLogKept requires every record an earlier manager reported to
// read back unchanged and in order, followed by that manager's close and
// the new manager's open.
func assertDaemonLogKept(t *testing.T, m *Manager, old []protocol.DaemonEvent) {
	t.Helper()
	st, err := m.Status("")
	if err != nil {
		t.Fatal(err)
	}
	events := st.Machine.DaemonEvents
	if len(events) < len(old)+2 || !reflect.DeepEqual(events[:len(old)], old) {
		t.Fatalf("daemon log after servicing:\n%+v\nbefore:\n%+v", events, old)
	}
	if rest := events[len(old):]; rest[0].Code != journal.DaemonEventClose || rest[1].Code != journal.DaemonEventOpen {
		t.Fatalf("daemon log after the earlier records: %+v", rest)
	}
}
