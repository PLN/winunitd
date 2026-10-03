package manager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

	st, err := after.Status("")
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, ev := range st.Machine.DaemonEvents {
		counts[ev.Code]++
	}
	if counts[journal.DaemonEventOpen] < 2 || counts[journal.DaemonEventClose] < 1 {
		t.Fatalf("daemon log after servicing: %v", counts)
	}
}
