//go:build windows

package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/eventlog"
	"github.com/PLN/winunitd/internal/unit"
)

func TestWindowsEventLogMatchStartsOneshot(t *testing.T) {
	skipIfEventLogUnavailable(t)
	id := windowsTestEventID(t)
	dir := t.TempDir()
	count := filepath.Join(dir, "count.txt")
	m := windowsEventLogManager(t, dir, id, `
Type=oneshot
`, "exit", 0, count)
	if _, err := m.Start(context.Background(), "foo.eventlog"); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "foo.eventlog", core.Active)
	reportApplication(t, id)
	waitWindowsCount(t, count, 1, 8*time.Second)
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
	reportApplication(t, id)
	waitWindowsCount(t, count, 2, 8*time.Second)
}

func TestWindowsEventLogUnrelatedEventIDDoesNotStart(t *testing.T) {
	skipIfEventLogUnavailable(t)
	match := windowsTestEventID(t)
	other := match + 1
	if other == 0 {
		other = match - 1
	}
	dir := t.TempDir()
	count := filepath.Join(dir, "count.txt")
	m := windowsEventLogManager(t, dir, match, `
Type=oneshot
`, "exit", 0, count)
	if _, err := m.Start(context.Background(), "foo.eventlog"); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "foo.eventlog", core.Active)
	reportApplication(t, other)
	time.Sleep(800 * time.Millisecond)
	if helperCountLines(count) != 0 {
		t.Fatal("unrelated EventID must not start the oneshot")
	}
}

func TestWindowsEventLogDoesNotRestartRunningSimple(t *testing.T) {
	skipIfEventLogUnavailable(t)
	id := windowsTestEventID(t)
	dir := t.TempDir()
	m := windowsEventLogManager(t, dir, id, `
Type=simple
`, "sleep", 0, "")
	if _, err := m.Start(context.Background(), "foo.service"); err != nil {
		t.Fatal(err)
	}
	proc := waitWindowsLiveProc(t, m, "foo.service")
	pid := proc.PID()
	gen := genOf(t, m, "foo.service")
	if _, err := m.Start(context.Background(), "foo.eventlog"); err != nil {
		t.Fatal(err)
	}
	reportApplication(t, id)
	time.Sleep(800 * time.Millisecond)
	got := waitWindowsLiveProc(t, m, "foo.service")
	if got.PID() != pid {
		t.Fatalf("running simple restarted; pid %d -> %d", pid, got.PID())
	}
	if gotGen := genOf(t, m, "foo.service"); gotGen != gen {
		t.Fatalf("gen = %d after matching event, want %d (C1 must not bump)", gotGen, gen)
	}
}

func TestWindowsEventLogUnknownChannelFailsConfiguration(t *testing.T) {
	if err := eventlog.WevtapiOK(); err != nil {
		t.Skip(err.Error())
	}
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
EventLogTrigger=winunitd-no-such-channel:EventID=1
`)
	m, err := New(Config{BaseDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	_, err = m.Start(context.Background(), "foo.eventlog")
	if err == nil {
		t.Fatal("unknown channel must fail")
	}
	assertState(t, m, "foo.eventlog", core.Failed)
	st, err := m.Status("foo.eventlog")
	if err != nil || st.Unit == nil || st.Unit.Reason != core.ReasonConfiguration {
		t.Fatalf("status = %+v err=%v", st, err)
	}
	ms, err := m.Status("")
	if err != nil || ms.Machine == nil || ms.Machine.State != "running" {
		t.Fatalf("daemon must stay up: %+v", ms)
	}
}

func TestWindowsDisableEventLogDoesNotStartOneshot(t *testing.T) {
	skipIfEventLogUnavailable(t)
	id := windowsTestEventID(t)
	dir := t.TempDir()
	count := filepath.Join(dir, "count.txt")
	m := windowsEventLogManager(t, dir, id, `
Type=oneshot
`, "exit", 0, count)
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
	reportApplication(t, id)
	time.Sleep(800 * time.Millisecond)
	if helperCountLines(count) != 0 {
		t.Fatal("disabled eventlog unit must not start the oneshot")
	}
}

func TestWindowsUserVerifyRejectsSystemEventLog(t *testing.T) {
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
	rep := unit.VerifyPath(filepath.Join(units, "foo.eventlog"))
	issues := unit.EventLogScopeIssues(rep.Unit, true)
	if len(issues) == 0 {
		t.Fatal("user verify must reject System")
	}
}

func windowsEventLogManager(t *testing.T, dir string, eventID uint16, serviceBody, helper string, exit int, countPath string) *Manager {
	t.Helper()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	exe, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	writeWindowsEventLogPair(t, units, dir, exe, eventID, serviceBody, helper, exit, countPath)
	m, err := New(Config{BaseDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	return m
}

func writeWindowsEventLogPair(t *testing.T, units, wd, exe string, eventID uint16, serviceBody, helper string, exit int, countPath string) {
	t.Helper()
	countEnv := ""
	if countPath != "" {
		countEnv = fmt.Sprintf("Environment=\"WINUNITD_JOB_COUNT=%s\"\n", countPath)
	}
	argv := []string{exe, winunitdHelperArgPrefix + helper}
	if helper == "sleep" {
		argv = windowsStayAliveArgv(t)
	}
	exeJSON, err := json.Marshal(argv)
	if err != nil {
		t.Fatal(err)
	}
	svc := fmt.Sprintf(""+
		"[Service]\n"+
		"%s"+
		"ExecStart=%s\n"+
		"WorkingDirectory=%s\n"+
		"Environment=WINUNITD_JOB_HELPER=%s\n"+
		"Environment=WINUNITD_JOB_EXIT=%d\n"+
		"%s",
		serviceBody, exeJSON, wd, helper, exit, countEnv)
	if err := os.WriteFile(filepath.Join(units, "foo.service"), []byte(svc), 0o644); err != nil {
		t.Fatal(err)
	}
	evt := fmt.Sprintf(""+
		"[EventLog]\n"+
		"EventLogTrigger=Application:EventID=%d\n"+
		"[Install]\n"+
		"WantedBy=default.target\n",
		eventID)
	if err := os.WriteFile(filepath.Join(units, "foo.eventlog"), []byte(evt), 0o644); err != nil {
		t.Fatal(err)
	}
}

func skipIfEventLogUnavailable(t *testing.T) {
	t.Helper()
	if err := eventlog.SubscribeOK(); err != nil {
		t.Skip(err.Error())
	}
}

func reportApplication(t *testing.T, id uint16) {
	t.Helper()
	if err := eventlog.ReportApplicationEvent(id, "winunitd t2 test"); err != nil {
		t.Fatal(err)
	}
}

func windowsTestEventID(t *testing.T) uint16 {
	t.Helper()
	id := uint16(40000 + time.Now().UnixNano()%20000)
	if id == 0 {
		id = 40001
	}
	return id
}
