package manager

import (
	"context"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/journal"
	"github.com/PLN/winunitd/internal/notify"
	"github.com/PLN/winunitd/internal/protocol"
)

func TestSuccessiveStartsGetDifferentInvocationIDs(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{stdout: "first run\n"}
	m := managerWith(t, launch, map[string]string{
		"foo.service": `
[Service]
Type=simple
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
	})
	ctx := context.Background()
	if _, err := m.Start(ctx, "foo"); err != nil {
		t.Fatal(err)
	}
	id1 := mustInvocationID(t, m, "foo")
	waitJournalMessage(t, m, "foo", "first run")
	logs1, err := m.Logs(protocol.LogsParams{Unit: "foo"})
	if err != nil {
		t.Fatal(err)
	}
	assertLogInvocation(t, logs1, "first run", id1)

	if _, err := m.Stop("foo"); err != nil {
		t.Fatal(err)
	}
	launch.mu.Lock()
	launch.stdout = "second run\n"
	launch.mu.Unlock()

	if _, err := m.Start(ctx, "foo"); err != nil {
		t.Fatal(err)
	}
	id2 := mustInvocationID(t, m, "foo")
	if id2 == id1 {
		t.Fatalf("second start reused InvocationID %s", id1)
	}
	waitJournalMessage(t, m, "foo", "second run")
	logs2, err := m.Logs(protocol.LogsParams{Unit: "foo"})
	if err != nil {
		t.Fatal(err)
	}
	assertLogInvocation(t, logs2, "first run", id1)
	assertLogInvocation(t, logs2, "second run", id2)
	for _, e := range logs2.Entries {
		if e.Message == "second run" && e.InvocationID == id1 {
			t.Fatalf("second-run line shared first id: %+v", e)
		}
	}
}

func TestRestartAlwaysGetsNewInvocationID(t *testing.T) {
	t.Parallel()
	launch := &scriptedLauncher{exitAll: intPtr(0)}
	m := managerWith(t, launch, map[string]string{
		"foo.service": `
[Service]
Type=simple
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
Restart=always
RestartSec=50ms
`,
	})
	if _, err := m.Start(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 2*time.Second, func() bool { return launch.nstarts() >= 1 })
	id1 := mustInvocationID(t, m, "foo")
	waitStarts(t, launch, 2, 2*time.Second)
	waitUntil(t, 2*time.Second, func() bool {
		st, err := m.Status("foo")
		return err == nil && st.Unit != nil && st.Unit.InvocationID != "" && st.Unit.InvocationID != id1
	})
	id2 := mustInvocationID(t, m, "foo")
	if id2 == id1 {
		t.Fatal("Restart= relaunch must get a new InvocationID")
	}
}

func TestStartInjectsInvocationIDEnv(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	m := managerWith(t, launch, map[string]string{
		"plain.service": `
[Service]
Type=simple
ExecStart=C:\Tools\plain.exe
WorkingDirectory=C:\Tools
`,
	})
	if _, err := m.Start(context.Background(), "plain"); err != nil {
		t.Fatal(err)
	}
	id := mustInvocationID(t, m, "plain")
	specs := launch.specs()
	if len(specs) != 1 {
		t.Fatalf("specs = %+v", specs)
	}
	got, ok := notify.LookupEnv(specs[0].Env, journal.EnvInvocationID)
	if !ok || got != id {
		t.Fatalf("env %s = %q ok=%v, want %s", journal.EnvInvocationID, got, ok, id)
	}
}

func TestNeverStartedUnitHasNoInvocationID(t *testing.T) {
	t.Parallel()
	m := testManager(t, map[string]string{
		"foo.service": `
[Service]
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
	})
	st, err := m.Status("foo")
	if err != nil {
		t.Fatal(err)
	}
	if st.Unit == nil || st.Unit.InvocationID != "" {
		t.Fatalf("never-started status = %+v", st.Unit)
	}
}

func TestDaemonReloadPreservesInvocationID(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{stdout: "keep me\n"}
	m := managerWith(t, launch, map[string]string{
		"foo.service": `
[Service]
Type=simple
ExecStart=C:\Tools\foo.exe
WorkingDirectory=C:\Tools
`,
	})
	if _, err := m.Start(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
	id := mustInvocationID(t, m, "foo")
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := mustInvocationID(t, m, "foo"); got != id {
		t.Fatalf("reload changed InvocationID %s -> %s", id, got)
	}
}

func mustInvocationID(t *testing.T, m *Manager, name string) string {
	t.Helper()
	st, err := m.Status(name)
	if err != nil {
		t.Fatal(err)
	}
	if st.Unit == nil || !journal.ValidInvocationID(st.Unit.InvocationID) {
		t.Fatalf("status InvocationID = %+v", st.Unit)
	}
	return st.Unit.InvocationID
}

func waitJournalMessage(t *testing.T, m *Manager, unit, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last *protocol.LogsResult
	for time.Now().Before(deadline) {
		logs, err := m.Logs(protocol.LogsParams{Unit: unit})
		if err != nil {
			t.Fatal(err)
		}
		last = logs
		for _, e := range logs.Entries {
			if e.Message == msg {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("journal missing %q: %+v", msg, last)
}

func assertLogInvocation(t *testing.T, logs *protocol.LogsResult, msg, id string) {
	t.Helper()
	found := false
	for _, e := range logs.Entries {
		if e.Message != msg {
			continue
		}
		found = true
		if e.InvocationID != id {
			t.Fatalf("line %q invocation = %q, want %q", msg, e.InvocationID, id)
		}
	}
	if !found {
		t.Fatalf("missing journal line %q in %+v", msg, logs)
	}
}
