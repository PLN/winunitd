package manager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PLN/winunitd/internal/journal"
	"github.com/PLN/winunitd/internal/protocol"
)

// A repair, upgrade or rollback stops the manager and starts a new instance
// on the same data. Identities survive in the durable records: journal lines
// keep their invocation ID and daemon.open records the build. Live operation
// history does not survive, and an old operation ID is never answered with a
// record from the new instance.
func TestIdentitiesAcrossManagerReplacement(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, units, "foo.service", "[Service]\nExecStart=C:\\Tools\\foo.exe\nWorkingDirectory=C:\\Tools\n")
	open := func(stdout string) *Manager {
		t.Helper()
		m, err := New(Config{BaseDir: dir, Launch: &fakeLauncher{stdout: stdout}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.Reload(); err != nil {
			t.Fatal(err)
		}
		return m
	}

	before := open("before servicing\n")
	res, err := before.Start(context.Background(), "foo")
	if err != nil {
		t.Fatal(err)
	}
	oldOperation := res.OperationID
	oldInvocation := mustInvocationID(t, before, "foo")
	waitJournalMessage(t, before, "foo", "before servicing")
	stopAll(before)

	after := open("after servicing\n")
	t.Cleanup(func() { stopAll(after) })
	if _, err := after.Operation(oldOperation); !isNotFound(err) {
		t.Fatalf("old operation %s after replacement: %v", oldOperation, err)
	}
	logs, err := after.Logs(protocol.LogsParams{Unit: "foo"})
	if err != nil {
		t.Fatal(err)
	}
	assertLogInvocation(t, logs, "before servicing", oldInvocation)

	res, err = after.Start(context.Background(), "foo")
	if err != nil {
		t.Fatal(err)
	}
	if res.OperationID == oldOperation || strings.SplitN(res.OperationID, "/op/", 2)[0] == strings.SplitN(oldOperation, "/op/", 2)[0] {
		t.Fatalf("operation namespace reused: %s then %s", oldOperation, res.OperationID)
	}
	if got := mustInvocationID(t, after, "foo"); got == oldInvocation {
		t.Fatalf("invocation ID reused across replacement: %s", got)
	}
	st, err := after.Status("")
	if err != nil {
		t.Fatal(err)
	}
	opens := 0
	for _, ev := range st.Machine.DaemonEvents {
		if ev.Code == journal.DaemonEventOpen {
			opens++
		}
	}
	if opens != 2 {
		t.Fatalf("daemon.open records = %d: %+v", opens, st.Machine.DaemonEvents)
	}
}
