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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/journal"
	"github.com/PLN/winunitd/internal/notify"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/unit"
	"golang.org/x/sys/windows"
)

// winunitdHelperArgPrefix is passed as ExecStart argv so a CreateProcess
// helper does not depend on WINUNITD_JOB_HELPER surviving the environment
// block. TestMain reads it before m.Run() (which would reject the unknown flag).
const winunitdHelperArgPrefix = "-winunitd-helper="

func helperMode() string {
	for _, a := range os.Args[1:] {
		if strings.HasPrefix(a, winunitdHelperArgPrefix) {
			return strings.TrimPrefix(a, winunitdHelperArgPrefix)
		}
	}
	return strings.TrimSpace(os.Getenv("WINUNITD_JOB_HELPER"))
}

func TestMain(m *testing.M) {
	switch helperMode() {
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
	case "print-invocation":
		fmt.Println(os.Getenv("WINUNIT_INVOCATION_ID"))
		_ = os.Stdout.Sync()
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
		cmd := exec.Command(os.Args[0], winunitdHelperArgPrefix+"sleep")
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
	case "notify-ready":
		if d := os.Getenv("WINUNITD_NOTIFY_DELAY"); d != "" {
			if n, err := strconv.Atoi(d); err == nil && n > 0 {
				time.Sleep(time.Duration(n) * time.Millisecond)
			}
		}
		if err := sendNotifyFromEnv(notify.Message{Ready: true}); err != nil {
			fmt.Fprintf(os.Stderr, "notify-ready: %v\n", err)
			os.Exit(1)
		}
		select {}
	case "notify-never":
		select {}
	case "notify-watchdog":
		if err := sendNotifyFromEnv(notify.Message{Ready: true}); err != nil {
			fmt.Fprintf(os.Stderr, "notify-watchdog ready: %v\n", err)
			os.Exit(1)
		}
		every := 50 * time.Millisecond
		if v := os.Getenv("WINUNITD_WATCHDOG_EVERY"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				every = time.Duration(n) * time.Millisecond
			}
		}
		for {
			_ = sendNotifyFromEnv(notify.Message{Watchdog: true})
			time.Sleep(every)
		}
	case "spawn-notify":
		exe := os.Getenv("WINUNIT_NOTIFY_EXE")
		if exe == "" {
			fmt.Fprintln(os.Stderr, "WINUNIT_NOTIFY_EXE is empty")
			os.Exit(1)
		}
		runNotify := func(args ...string) error {
			cmd := exec.Command(exe, args...)
			cmd.Env = os.Environ()
			cmd.SysProcAttr = &windows.SysProcAttr{
				HideWindow:    true,
				CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
			}
			return cmd.Run()
		}
		var last error
		for i := 0; i < 30; i++ {
			last = runNotify("--ready")
			if last == nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if last != nil {
			fmt.Fprintf(os.Stderr, "winunit-notify --ready: %v\n", last)
			os.Exit(1)
		}
		if os.Getenv("WINUNITD_NOTIFY_WATCHDOG") != "" {
			go func() {
				for {
					_ = runNotify("--watchdog")
					time.Sleep(50 * time.Millisecond)
				}
			}()
		}
		select {}
	case "scm-proxy":
		runManagerSCMProxyTestService()
		os.Exit(0)
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

func sendNotifyFromEnv(msg notify.Message) error {
	addr := os.Getenv(notify.EnvNotifyPipe)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return notify.SendRetry(ctx, addr, msg)
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
	exeJSON, err := json.Marshal([]string{exe, winunitdHelperArgPrefix + "spawn"})
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
	t.Cleanup(func() {
		_, _ = m.Stop("tree")
		m.Close()
	})

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
	argv := []string{exe, winunitdHelperArgPrefix + helper}
	if helper == "sleep" {
		argv = windowsStayAliveArgv(t)
	}
	exeJSON, err := json.Marshal(argv)
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
	t.Cleanup(func() {
		_, _ = m.Stop(name)
		m.Close()
	})
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
	t.Cleanup(m.Close)
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
		mustJSONArgv(t, os.Args[0], "sleep"), dir)
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
		mustJSONArgv(t, os.Args[0], "sleep"), dir)
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

func windowsStayAliveArgv(t *testing.T) []string {
	t.Helper()
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	ping := filepath.Join(root, "System32", "ping.exe")
	if _, err := os.Stat(ping); err != nil {
		t.Fatalf("ping.exe: %v", err)
	}
	// ICMP ping, not a TCP listener. The unit only needs a process that
	// stays alive while the parent holds the watchdog endpoint.
	return []string{ping, "-t", "127.0.0.1"}
}

func mustJSONArgv(t *testing.T, exe string, helper string) string {
	t.Helper()
	var argv []string
	if helper == "sleep" {
		argv = windowsStayAliveArgv(t)
	} else {
		abs, err := filepath.Abs(exe)
		if err != nil {
			t.Fatal(err)
		}
		argv = []string{abs}
		if helper != "" {
			argv = append(argv, winunitdHelperArgPrefix+helper)
		}
	}
	b, err := json.Marshal(argv)
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
	if !journal.ValidInvocationID(st.Unit.InvocationID) {
		t.Fatalf("status InvocationID = %q", st.Unit.InvocationID)
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
			for _, e := range logs.Entries {
				if e.InvocationID != st.Unit.InvocationID {
					t.Fatalf("journal invocation %q != status %q", e.InvocationID, st.Unit.InvocationID)
				}
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("journal = %+v", logs)
}

func TestWindowsInvocationIDsDifferAcrossStarts(t *testing.T) {
	dir := t.TempDir()
	m := startWindowsHelperUnit(t, dir, "inv.service", `
Type=simple
`, "print-invocation", 0, "")
	if _, err := m.Start(context.Background(), "inv"); err != nil {
		t.Fatal(err)
	}
	_ = waitWindowsLiveProc(t, m, "inv.service")
	st1, err := m.Status("inv")
	if err != nil {
		t.Fatal(err)
	}
	if st1.Unit == nil || !journal.ValidInvocationID(st1.Unit.InvocationID) {
		t.Fatalf("first status = %+v", st1.Unit)
	}
	id1 := st1.Unit.InvocationID
	waitWindowsJournalMessage(t, m, "inv", id1)
	logs1, err := m.Logs(protocol.LogsParams{Unit: "inv"})
	if err != nil {
		t.Fatal(err)
	}
	n1 := len(logs1.Entries)
	if n1 == 0 {
		t.Fatal("first run produced no journal lines")
	}
	for _, e := range logs1.Entries {
		if e.InvocationID != id1 {
			t.Fatalf("first-run line %+v", e)
		}
	}

	if _, err := m.Stop("inv"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(context.Background(), "inv"); err != nil {
		t.Fatal(err)
	}
	_ = waitWindowsLiveProc(t, m, "inv.service")
	st2, err := m.Status("inv")
	if err != nil {
		t.Fatal(err)
	}
	if st2.Unit == nil || !journal.ValidInvocationID(st2.Unit.InvocationID) {
		t.Fatalf("second status = %+v", st2.Unit)
	}
	id2 := st2.Unit.InvocationID
	if id2 == id1 {
		t.Fatalf("second start reused InvocationID %s", id1)
	}
	waitWindowsJournalMessage(t, m, "inv", id2)
	logs2, err := m.Logs(protocol.LogsParams{Unit: "inv"})
	if err != nil {
		t.Fatal(err)
	}
	if len(logs2.Entries) <= n1 {
		t.Fatalf("second run added no journal lines: %+v", logs2.Entries)
	}
	for _, e := range logs2.Entries[n1:] {
		if e.InvocationID == id1 {
			t.Fatalf("second-run line shared first id: %+v", e)
		}
		if e.InvocationID != id2 {
			t.Fatalf("second-run line %+v, want %s", e, id2)
		}
	}
	for _, e := range logs2.Entries[:n1] {
		if e.InvocationID != id1 {
			t.Fatalf("first-run line rewritten: %+v", e)
		}
	}
}

func TestWindowsRestartAlwaysGetsNewInvocationID(t *testing.T) {
	m, count := startWindowsRestartUnit(t, "always", 0, "count-then-sleep")
	st1, err := m.Status("foo")
	if err != nil {
		t.Fatal(err)
	}
	if st1.Unit == nil || !journal.ValidInvocationID(st1.Unit.InvocationID) {
		t.Fatalf("first status = %+v", st1.Unit)
	}
	id1 := st1.Unit.InvocationID
	waitWindowsCount(t, count, 2, 5*time.Second)
	_ = waitWindowsLiveProc(t, m, "foo.service")
	deadline := time.Now().Add(5 * time.Second)
	var id2 string
	for time.Now().Before(deadline) {
		st2, err := m.Status("foo")
		if err != nil {
			t.Fatal(err)
		}
		if st2.Unit != nil && st2.Unit.InvocationID != "" && st2.Unit.InvocationID != id1 {
			id2 = st2.Unit.InvocationID
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !journal.ValidInvocationID(id2) || id2 == id1 {
		t.Fatalf("Restart= relaunch InvocationID = %q (first %s)", id2, id1)
	}
}

func waitWindowsJournalMessage(t *testing.T, m *Manager, unit, msg string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
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
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("journal missing %q: %+v", msg, last)
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

func TestWindowsTimerOneshotOnStartupSec(t *testing.T) {
	dir := t.TempDir()
	count := filepath.Join(dir, "count.txt")
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := mustJSONArgv(t, os.Args[0], "exit")
	svc := fmt.Sprintf(""+
		"[Service]\n"+
		"Type=oneshot\n"+
		"ExecStart=%s\n"+
		"WorkingDirectory=%s\n"+
		"Environment=WINUNITD_JOB_HELPER=exit\n"+
		"Environment=WINUNITD_JOB_EXIT=0\n"+
		"Environment=\"WINUNITD_JOB_COUNT=%s\"\n",
		exe, dir, count)
	if err := os.WriteFile(filepath.Join(units, "job.service"), []byte(svc), 0o644); err != nil {
		t.Fatal(err)
	}
	timer := "" +
		"[Timer]\n" +
		"OnStartupSec=200ms\n" +
		"[Install]\n" +
		"WantedBy=timers.target\n"
	if err := os.WriteFile(filepath.Join(units, "job.timer"), []byte(timer), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := New(Config{BaseDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	if _, err := m.Enable("job.timer"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitWindowsCount(t, count, 1, 5*time.Second)
	// The helper writes the count file inside CreateProcess, before the
	// timer fire goroutine (engine: go fire → onTimerElapsed → Start)
	// has applyRunLocked Active. Checking state on the next line was a
	// race (failing SHA 615fdb01 lasted 0.24s ≈ OnStartupSec). This test
	// never Stop/Shutdown's; Boot only Starts default.target. Oneshot
	// exit 0 still stays Active: TestWindowsOneshotRestartOnFailureIgnoresExit0
	// passed on that same Windows run. Wait for the M9 assertion.
	deadline := time.Now().Add(5 * time.Second)
	var st, timerSt core.State
	var stopping bool
	for time.Now().Before(deadline) {
		m.mu.Lock()
		st = m.stateOfLocked("job.service")
		timerSt = m.stateOfLocked("job.timer")
		stopping = m.stopping["job.service"]
		m.mu.Unlock()
		if st == core.Active && timerSt == core.Active {
			if stopping {
				t.Fatal("oneshot is Active but stopping; Stop leaked into timer activation")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	if stopping {
		t.Fatalf("oneshot was stopped (state=%s); not an applyRunLocked race", st)
	}
	if st != core.Active {
		t.Fatalf("oneshot activated by timer state = %s", st)
	}
	if timerSt != core.Active {
		t.Fatalf("timer state = %s", timerSt)
	}
}

func TestWindowsOnBootSecVsOnStartupSec(t *testing.T) {
	dir := t.TempDir()
	bootCount := filepath.Join(dir, "boot.txt")
	startCount := filepath.Join(dir, "start.txt")
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := mustJSONArgv(t, os.Args[0], "exit")
	writeTimerPair := func(base, trigger, countPath string) {
		t.Helper()
		svc := fmt.Sprintf(""+
			"[Service]\n"+
			"Type=oneshot\n"+
			"ExecStart=%s\n"+
			"WorkingDirectory=%s\n"+
			"Environment=WINUNITD_JOB_HELPER=exit\n"+
			"Environment=WINUNITD_JOB_EXIT=0\n"+
			"Environment=\"WINUNITD_JOB_COUNT=%s\"\n",
			exe, dir, countPath)
		if err := os.WriteFile(filepath.Join(units, base+".service"), []byte(svc), 0o644); err != nil {
			t.Fatal(err)
		}
		tm := fmt.Sprintf(""+
			"[Timer]\n"+
			"%s\n"+
			"[Install]\n"+
			"WantedBy=timers.target\n", trigger)
		if err := os.WriteFile(filepath.Join(units, base+".timer"), []byte(tm), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeTimerPair("boot", "OnBootSec=10ms", bootCount)
	writeTimerPair("start", "OnStartupSec=10s", startCount)

	m, err := New(Config{BaseDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	if _, err := m.Enable("boot.timer"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Enable("start.timer"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitWindowsCount(t, bootCount, 1, 5*time.Second)
	time.Sleep(400 * time.Millisecond)
	if n := helperCountLines(startCount); n != 0 {
		t.Fatalf("OnStartupSec fired too early (starts=%d); OnBootSec and OnStartupSec must differ after a long machine uptime", n)
	}
}

func TestWindowsShutdownStopsAfterOrderedServicesAndClosesJob(t *testing.T) {
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := mustJSONArgv(t, os.Args[0], "sleep")
	writeSleep := func(name, extra string) {
		t.Helper()
		body := fmt.Sprintf(""+
			"[Unit]\n"+
			"%s"+
			"[Service]\n"+
			"Type=simple\n"+
			"ExecStart=%s\n"+
			"WorkingDirectory=%s\n"+
			"Environment=WINUNITD_JOB_HELPER=sleep\n",
			extra, exe, dir)
		if err := os.WriteFile(filepath.Join(units, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeSleep("db.service", "")
	writeSleep("web.service", "Requires=db.service\nAfter=db.service\n")

	job, err := runtime.OpenDaemonJob()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = job.Close() })
	launch := &orderLaunch{inner: runtime.NewLauncher(job)}
	m, err := New(Config{BaseDir: dir, Launch: launch, Daemon: job})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })

	if _, err := m.Start(context.Background(), "web"); err != nil {
		t.Fatal(err)
	}
	web := waitWindowsLiveProc(t, m, "web.service")
	db := waitWindowsLiveProc(t, m, "db.service")
	webPID, dbPID := web.PID(), db.PID()

	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := launch.stopped()
	if len(got) < 2 || got[0] != "web.service" || got[1] != "db.service" {
		t.Fatalf("stop order = %v, want web then db", got)
	}
	assertState(t, m, "web.service", core.Inactive)
	assertState(t, m, "db.service", core.Inactive)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !windowsProcessAlive(webPID) && !windowsProcessAlive(dbPID) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if windowsProcessAlive(webPID) || windowsProcessAlive(dbPID) {
		t.Fatal("unit processes still alive after ordered stop")
	}

	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	if !job.Closed() {
		t.Fatal("daemon job must be closed after ordered stop")
	}
}

func TestWindowsPreshutdownPathStopsUnitsAndClosesJob(t *testing.T) {
	// Simulated SCM/preshutdown without a live SCM: cancel the run
	// context (what host.Execute does on PreShutdown), then the same
	// finish sequence as serve() — Shutdown then Close.
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := mustJSONArgv(t, os.Args[0], "sleep")
	body := fmt.Sprintf(""+
		"[Service]\n"+
		"Type=simple\n"+
		"ExecStart=%s\n"+
		"WorkingDirectory=%s\n"+
		"Environment=WINUNITD_JOB_HELPER=sleep\n",
		exe, dir)
	if err := os.WriteFile(filepath.Join(units, "leaf.service"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	job, err := runtime.OpenDaemonJob()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	m, err := New(Config{BaseDir: dir, Daemon: job})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(ctx, "leaf"); err != nil {
		t.Fatal(err)
	}
	proc := waitWindowsLiveProc(t, m, "leaf.service")
	pid := proc.PID()

	cancel() // SERVICE_CONTROL_PRESHUTDOWN / SIGINT
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	m.Close()
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	if !job.Closed() {
		t.Fatal("daemon job still open after simulated preshutdown")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !windowsProcessAlive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("leaf process outlived the closed daemon job")
}

type orderLaunch struct {
	inner runtime.Launcher
	mu    sync.Mutex
	stops []string
}

type orderProc struct {
	runtime.Process
	name string
	rec  *orderLaunch
}

func (l *orderLaunch) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	p, err := l.inner.Start(ctx, spec)
	if err != nil {
		return nil, err
	}
	return &orderProc{Process: p, name: spec.Unit, rec: l}, nil
}

func (l *orderLaunch) stopped() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.stops...)
}

func (p *orderProc) Stop(d time.Duration) error {
	p.rec.mu.Lock()
	p.rec.stops = append(p.rec.stops, p.name)
	p.rec.mu.Unlock()
	return p.Process.Stop(d)
}

func TestWindowsNotifyStaysActivatingUntilReady(t *testing.T) {
	dir := t.TempDir()
	m := startWindowsHelperUnit(t, dir, "p4ready.service", `
Type=notify
TimeoutStartSec=5s
Environment=WINUNITD_NOTIFY_DELAY=300
`, "notify-ready", 0, "")

	errc := make(chan error, 1)
	go func() {
		_, err := m.Start(context.Background(), "p4ready")
		errc <- err
	}()
	waitUntil(t, 2*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.stateOfLocked("p4ready.service") == core.Activating && m.procs["p4ready.service"] != nil
	})
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "p4ready.service", core.Active)
}

func TestWindowsNotifyTimeoutStartWithoutReadyFails(t *testing.T) {
	dir := t.TempDir()
	m := startWindowsHelperUnit(t, dir, "p4late.service", `
Type=notify
TimeoutStartSec=300ms
`, "notify-never", 0, "")
	_, err := m.Start(context.Background(), "p4late")
	if err == nil {
		t.Fatal("expected TimeoutStartSec failure")
	}
	if !strings.Contains(err.Error(), "READY") && !strings.Contains(err.Error(), "TimeoutStartSec") {
		t.Fatalf("err = %v", err)
	}
	assertState(t, m, "p4late.service", core.Failed)
}

func TestWindowsWatchdogPulseKeepsUnitActive(t *testing.T) {
	dir := t.TempDir()
	m := startWindowsHelperUnit(t, dir, "p4hb.service", `
Type=notify
TimeoutStartSec=5s
WatchdogSec=300ms
Environment=WINUNITD_WATCHDOG_EVERY=50
`, "notify-watchdog", 0, "")
	if _, err := m.Start(context.Background(), "p4hb"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond)
	assertState(t, m, "p4hb.service", core.Active)
}

func TestWindowsMissedWatchdogSecFailsUnit(t *testing.T) {
	dir := t.TempDir()
	m := startWindowsHelperUnit(t, dir, "p4miss.service", `
Type=notify
TimeoutStartSec=5s
WatchdogSec=200ms
Restart=no
`, "notify-ready", 0, "")
	if _, err := m.Start(context.Background(), "p4miss"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 3*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.stateOfLocked("p4miss.service") == core.Failed
	})
	m.mu.Lock()
	sub := m.subOfLocked("p4miss.service")
	err := m.errors["p4miss.service"]
	m.mu.Unlock()
	if sub != core.SubWatchdog {
		t.Fatalf("sub = %s error=%q", sub, err)
	}
}

func TestWindowsWinunitNotifyReadyAgainstLiveManager(t *testing.T) {
	exe := buildWinunitNotify(t)
	dir := t.TempDir()
	m := startWindowsHelperUnit(t, dir, "p4cli.service", fmt.Sprintf(""+
		"Type=notify\n"+
		"TimeoutStartSec=5s\n"+
		"WatchdogSec=300ms\n"+
		"Environment=\"WINUNIT_NOTIFY_EXE=%s\"\n"+
		"Environment=WINUNITD_NOTIFY_WATCHDOG=1\n", exe), "spawn-notify", 0, "")
	if _, err := m.Start(context.Background(), "p4cli"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond)
	assertState(t, m, "p4cli.service", core.Active)
}

func buildWinunitNotify(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "winunit-notify.exe")
	cmd := exec.Command("go", "build", "-o", out, "github.com/PLN/winunitd/cmd/winunit-notify")
	cmd.Dir = findModuleRoot(t)
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build winunit-notify: %v\n%s", err, b)
	}
	return out
}

func findModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func TestWindowsTCPWatchdogConnectKeepsUnitActive(t *testing.T) {
	_, addr := listenLoopbackTCP(t)
	dir := t.TempDir()
	m := startWindowsHelperUnit(t, dir, "p5tcp.service", fmt.Sprintf(`
Type=simple
WatchdogMode=tcp
WatchdogEndpoint=%s
WatchdogSec=300ms
Restart=no
`, addr), "sleep", 0, "")
	requireLoadedWatchdog(t, m, "p5tcp.service", unit.WatchdogModeTCP, addr)
	if _, err := m.Start(context.Background(), "p5tcp"); err != nil {
		t.Fatal(err)
	}
	_ = waitWindowsLiveProc(t, m, "p5tcp.service")
	time.Sleep(800 * time.Millisecond)
	assertWatchdogActive(t, m, "p5tcp.service")
}

func TestWindowsTCPWatchdogRefusedFails(t *testing.T) {
	addr := closedLoopbackTCP(t)
	dir := t.TempDir()
	m := startWindowsHelperUnit(t, dir, "p5refused.service", fmt.Sprintf(`
Type=simple
WatchdogMode=tcp
WatchdogEndpoint=%s
WatchdogSec=200ms
Restart=no
`, addr), "sleep", 0, "")
	requireLoadedWatchdog(t, m, "p5refused.service", unit.WatchdogModeTCP, addr)
	if _, err := m.Start(context.Background(), "p5refused"); err != nil {
		t.Fatal(err)
	}
	waitWatchdogFailed(t, m, "p5refused.service")
}

func TestWindowsTCPWatchdogWrongHostNotLoaded(t *testing.T) {
	dir := t.TempDir()
	m := startWindowsHelperUnit(t, dir, "p5bad.service", `
Type=simple
WatchdogMode=tcp
WatchdogEndpoint=192.0.2.1:80
WatchdogSec=1s
Restart=no
`, "sleep", 0, "")
	_, err := m.Start(context.Background(), "p5bad")
	if err == nil {
		t.Fatal("non-loopback WatchdogEndpoint must not load")
	}
}

func TestWindowsHTTPWatchdog200KeepsUnitActive(t *testing.T) {
	ep := serveWatchdogHTTP(t, 200)
	dir := t.TempDir()
	m := startWindowsHelperUnit(t, dir, "p5http.service", fmt.Sprintf(`
Type=simple
WatchdogMode=http
WatchdogEndpoint=%s
WatchdogSec=300ms
Restart=no
`, ep), "sleep", 0, "")
	requireLoadedWatchdog(t, m, "p5http.service", unit.WatchdogModeHTTP, "")
	if _, err := m.Start(context.Background(), "p5http"); err != nil {
		t.Fatal(err)
	}
	_ = waitWindowsLiveProc(t, m, "p5http.service")
	time.Sleep(800 * time.Millisecond)
	assertWatchdogActive(t, m, "p5http.service")
}

func TestWindowsHTTPWatchdogWrongStatusFails(t *testing.T) {
	ep := serveWatchdogHTTP(t, 503)
	dir := t.TempDir()
	m := startWindowsHelperUnit(t, dir, "p5httpbad.service", fmt.Sprintf(`
Type=simple
WatchdogMode=http
WatchdogEndpoint=%s
WatchdogExpectedStatus=200
WatchdogSec=200ms
Restart=no
`, ep), "sleep", 0, "")
	requireLoadedWatchdog(t, m, "p5httpbad.service", unit.WatchdogModeHTTP, "")
	if _, err := m.Start(context.Background(), "p5httpbad"); err != nil {
		t.Fatal(err)
	}
	waitWatchdogFailed(t, m, "p5httpbad.service")
}

func requireLoadedWatchdog(t *testing.T, m *Manager, name string, mode unit.WatchdogMode, addr string) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	ld := m.units[name]
	if ld == nil || ld.unit == nil || ld.unit.Service == nil {
		t.Fatalf("%s not loaded", name)
	}
	svc := ld.unit.Service
	if svc.WatchdogMode != mode {
		t.Fatalf("WatchdogMode = %s, want %s", svc.WatchdogMode, mode)
	}
	if addr != "" && svc.WatchdogAddr != addr {
		t.Fatalf("WatchdogAddr = %q, want %q", svc.WatchdogAddr, addr)
	}
}
