//go:build windows

package manager

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
	"golang.org/x/sys/windows"
)

func TestMain(m *testing.M) {
	switch os.Getenv("WINUNITD_JOB_HELPER") {
	case "sleep":
		select {}
	case "print":
		fmt.Println(os.Getenv("WINUNITD_JOB_PRINT"))
		_ = os.Stdout.Sync()
		if msg := os.Getenv("WINUNITD_JOB_PRINT_ERR"); msg != "" {
			fmt.Fprintln(os.Stderr, msg)
			_ = os.Stderr.Sync()
		}
		select {}
	case "exit":
		recordHelperCount()
		os.Exit(helperExitCode())
	case "count-then-sleep":
		recordHelperCount()
		n := helperCountLines(os.Getenv("WINUNITD_JOB_COUNT"))
		sleepAfter := 2
		if v := os.Getenv("WINUNITD_JOB_SLEEP_AFTER"); v != "" {
			if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
				sleepAfter = parsed
			}
		}
		if n >= sleepAfter {
			select {}
		}
		os.Exit(helperExitCode())
	case "spawn":
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), "WINUNITD_JOB_HELPER=sleep")
		cmd.SysProcAttr = &windows.SysProcAttr{
			HideWindow:    true,
			CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
		}
		if err := cmd.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "spawn: %v\n", err)
			os.Exit(1)
		}
		select {}
	}
	os.Exit(m.Run())
}

func recordHelperCount() {
	path := os.Getenv("WINUNITD_JOB_COUNT")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	fmt.Fprintf(f, "%d\n", os.Getpid())
	_ = f.Close()
}

func helperExitCode() int {
	v := os.Getenv("WINUNITD_JOB_EXIT")
	if v == "" {
		return 0
	}
	n, _ := strconv.Atoi(v)
	return n
}

func helperCountLines(path string) int {
	if path == "" {
		return 0
	}
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if sc.Text() != "" {
			n++
		}
	}
	return n
}

func TestManagerStartUnitJobAndKillTree(t *testing.T) {
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	exe, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	exeJSON, err := json.Marshal([]string{exe})
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(""+
		"[Service]\n"+
		"Type=simple\n"+
		"ExecStart=%s\n"+
		"WorkingDirectory=%s\n"+
		"Environment=WINUNITD_JOB_HELPER=spawn\n",
		exeJSON, dir)
	if err := os.WriteFile(filepath.Join(units, "tree.service"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := New(Config{BaseDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(context.Background(), "tree"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.Stop("tree") })

	m.mu.Lock()
	proc := m.procs["tree.service"]
	m.mu.Unlock()
	if proc == nil {
		t.Fatal("manager did not keep the started process")
	}
	in, err := proc.Job().Contains(proc.PID())
	if err != nil {
		t.Fatal(err)
	}
	if !in {
		t.Fatal("started unit is not in its unit job")
	}

	var pids []int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		pids, err = proc.Job().PIDs()
		if err == nil && len(pids) >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(pids) < 2 {
		t.Fatalf("unit job pids = %v (want parent + grandchild)", pids)
	}

	if _, err := m.Stop("tree"); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		alive := false
		for _, pid := range pids {
			if windowsProcessAlive(pid) {
				alive = true
				break
			}
		}
		if !alive {
			m.mu.Lock()
			st := m.states["tree.service"]
			m.mu.Unlock()
			if st != core.Inactive {
				t.Fatalf("state = %s", st)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("killing the unit job did not tear down the tree")
}

func windowsProcessAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == 259
}

func TestWindowsRestartAlwaysRelaunchesAfterExit0(t *testing.T) {
	m, count := startWindowsRestartUnit(t, "always", 0, "count-then-sleep")
	waitWindowsCount(t, count, 2, 5*time.Second)
	proc := waitWindowsLiveProc(t, m, "foo.service")
	in, err := proc.Job().Contains(proc.PID())
	if err != nil {
		t.Fatal(err)
	}
	if !in {
		t.Fatal("relaunched unit is not in a new unit job")
	}
	flags, err := proc.Job().LimitFlags()
	if err != nil {
		t.Fatal(err)
	}
	if flags&windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK != 0 || flags&windows.JOB_OBJECT_LIMIT_SILENT_BREAKAWAY_OK != 0 {
		t.Fatal("restarted unit job must not allow breakaway")
	}
}

func TestWindowsRestartOnFailureDoesNotRelaunchAfter0(t *testing.T) {
	_, count := startWindowsRestartUnit(t, "on-failure", 0, "exit")
	time.Sleep(400 * time.Millisecond)
	if n := helperCountLines(count); n != 1 {
		t.Fatalf("starts = %d, want 1", n)
	}
}

func TestWindowsRestartOnFailureRelaunchesAfterNonZero(t *testing.T) {
	m, count := startWindowsRestartUnit(t, "on-failure", 2, "count-then-sleep")
	waitWindowsCount(t, count, 2, 5*time.Second)
	_ = waitWindowsLiveProc(t, m, "foo.service")
}

func TestWindowsRestartNoNeverRelaunches(t *testing.T) {
	_, count := startWindowsRestartUnit(t, "no", 2, "exit")
	time.Sleep(400 * time.Millisecond)
	if n := helperCountLines(count); n != 1 {
		t.Fatalf("starts = %d, want 1", n)
	}
}

func TestWindowsExplicitStopStaysDown(t *testing.T) {
	dir := t.TempDir()
	m := startWindowsHelperUnit(t, dir, "foo.service", `
Type=simple
Restart=always
RestartSec=100ms
`, "sleep", 0, "")
	if _, err := m.Start(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
	_ = waitWindowsLiveProc(t, m, "foo.service")
	if _, err := m.Stop("foo"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		st := m.stateOfLocked("foo.service")
		proc := m.procs["foo.service"]
		m.mu.Unlock()
		if st != core.Inactive || proc != nil {
			t.Fatalf("after stop: state=%s proc=%v", st, proc != nil)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestWindowsOneshotRestartOnFailureIgnoresExit0(t *testing.T) {
	dir := t.TempDir()
	count := filepath.Join(dir, "count.txt")
	m := startWindowsHelperUnit(t, dir, "init.service", `
Type=oneshot
Restart=on-failure
RestartSec=100ms
`, "exit", 0, count)
	if _, err := m.Start(context.Background(), "init"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if n := helperCountLines(count); n != 1 {
		t.Fatalf("oneshot exit 0 is not a crash; starts = %d", n)
	}
	m.mu.Lock()
	st := m.stateOfLocked("init.service")
	m.mu.Unlock()
	if st != core.Active {
		t.Fatalf("oneshot state = %s", st)
	}
}

func startWindowsRestartUnit(t *testing.T, policy string, exit int, helper string) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	count := filepath.Join(dir, "count.txt")
	body := fmt.Sprintf(""+
		"Type=simple\n"+
		"Restart=%s\n"+
		"RestartSec=50ms\n",
		policy)
	m := startWindowsHelperUnit(t, dir, "foo.service", body, helper, exit, count)
	if _, err := m.Start(context.Background(), "foo"); err != nil {
		t.Fatal(err)
	}
	return m, count
}

func startWindowsHelperUnit(t *testing.T, dir, name, serviceBody, helper string, exit int, countPath string) *Manager {
	t.Helper()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	exe, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	exeJSON, err := json.Marshal([]string{exe})
	if err != nil {
		t.Fatal(err)
	}
	countEnv := ""
	if countPath != "" {
		countEnv = fmt.Sprintf("Environment=\"WINUNITD_JOB_COUNT=%s\"\n", countPath)
	}
	body := fmt.Sprintf(""+
		"[Service]\n"+
		"%s"+
		"ExecStart=%s\n"+
		"WorkingDirectory=%s\n"+
		"Environment=WINUNITD_JOB_HELPER=%s\n"+
		"Environment=WINUNITD_JOB_EXIT=%d\n"+
		"%s",
		serviceBody, exeJSON, dir, helper, exit, countEnv)
	if err := os.WriteFile(filepath.Join(units, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := New(Config{BaseDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.Stop(name) })
	return m
}

func waitWindowsCount(t *testing.T, path string, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var got int
	for time.Now().Before(deadline) {
		got = helperCountLines(path)
		if got >= n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("count = %d, want >= %d", got, n)
}

func TestWindowsEnableFileLayout(t *testing.T) {
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "" +
		"[Service]\n" +
		"ExecStart=C:\\Tools\\hermes.exe\n" +
		"WorkingDirectory=C:\\Tools\n" +
		"[Install]\n" +
		"WantedBy=default.target\n"
	if err := os.WriteFile(filepath.Join(units, "hermes.service"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := New(Config{BaseDir: dir, Launch: runtime.StubLauncher()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Enable("hermes.service"); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "enabled", "default.target", "hermes.service")
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("enable file must not be an NTFS symlink")
	}
	attrs, err := windows.GetFileAttributes(windows.StringToUTF16Ptr(path))
	if err != nil {
		t.Fatal(err)
	}
	if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		t.Fatal("enable file must not be a reparse point")
	}
	if attrs&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		t.Fatal("enable file must be a regular file")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hermes.service\n" {
		t.Fatalf("body = %q", got)
	}

	if _, err := m.Disable("hermes.service"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("disable did not remove %s: %v", path, err)
	}
}

func TestWindowsBootStartsOnlyEnabledUnit(t *testing.T) {
	dir := t.TempDir()
	on := startWindowsHelperUnit(t, dir, "on.service", `
Type=simple
`, "sleep", 0, "")
	offBody := fmt.Sprintf(""+
		"[Service]\n"+
		"Type=simple\n"+
		"ExecStart=%s\n"+
		"WorkingDirectory=%s\n"+
		"Environment=WINUNITD_JOB_HELPER=sleep\n"+
		"[Install]\n"+
		"WantedBy=default.target\n",
		mustJSONArgv(t, os.Args[0]), dir)
	if err := os.WriteFile(filepath.Join(dir, "units", "off.service"), []byte(offBody), 0o644); err != nil {
		t.Fatal(err)
	}
	// Rewrite on.service with WantedBy so enable has a target.
	onBody := fmt.Sprintf(""+
		"[Service]\n"+
		"Type=simple\n"+
		"ExecStart=%s\n"+
		"WorkingDirectory=%s\n"+
		"Environment=WINUNITD_JOB_HELPER=sleep\n"+
		"[Install]\n"+
		"WantedBy=default.target\n",
		mustJSONArgv(t, os.Args[0]), dir)
	if err := os.WriteFile(filepath.Join(dir, "units", "on.service"), []byte(onBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := on.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, err := on.Enable("on.service"); err != nil {
		t.Fatal(err)
	}
	if _, err := on.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = waitWindowsLiveProc(t, on, "on.service")
	on.mu.Lock()
	offProc := on.procs["off.service"]
	stOff := on.stateOfLocked("off.service")
	stOn := on.stateOfLocked("on.service")
	on.mu.Unlock()
	if offProc != nil || stOff != core.Inactive {
		t.Fatalf("disabled unit started: proc=%v state=%s", offProc != nil, stOff)
	}
	if stOn != core.Active {
		t.Fatalf("enabled unit state = %s", stOn)
	}
}

func mustJSONArgv(t *testing.T, exe string) string {
	t.Helper()
	abs, err := filepath.Abs(exe)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal([]string{abs})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func waitWindowsLiveProc(t *testing.T, m *Manager, name string) runtime.Process {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		proc := m.procs[name]
		m.mu.Unlock()
		if proc != nil && proc.Alive() {
			return proc
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("unit did not stay running after restart")
	return nil
}

func TestWindowsJournalWriteAndRead(t *testing.T) {
	dir := t.TempDir()
	m := startWindowsHelperUnit(t, dir, "log.service", `
Type=simple
Environment="WINUNITD_JOB_PRINT=hello from journal"
Environment="WINUNITD_JOB_PRINT_ERR=warn from journal"
`, "print", 0, "")
	if _, err := m.Start(context.Background(), "log"); err != nil {
		t.Fatal(err)
	}
	proc := waitWindowsLiveProc(t, m, "log.service")
	if proc.PID() <= 0 {
		t.Fatalf("pid = %d", proc.PID())
	}

	st, err := m.Status("log")
	if err != nil {
		t.Fatal(err)
	}
	if st.Unit == nil || st.Unit.ActiveState != "active" || st.Unit.MainPID != proc.PID() {
		t.Fatalf("status = %+v", st.Unit)
	}

	var logs *protocol.LogsResult
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		logs, err = m.Logs(protocol.LogsParams{Unit: "log"})
		if err != nil {
			t.Fatal(err)
		}
		if hasLogMessage(logs, "hello from journal") && hasLogMessage(logs, "warn from journal") {
			path := filepath.Join(dir, "journal", "log.service.log")
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("per-unit journal file: %v", err)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("journal = %+v", logs)
}

func hasLogMessage(logs *protocol.LogsResult, msg string) bool {
	if logs == nil {
		return false
	}
	for _, e := range logs.Entries {
		if e.Message == msg {
			return true
		}
	}
	return false
}
