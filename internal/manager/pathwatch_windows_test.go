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
	"github.com/PLN/winunitd/internal/pathwatch"
)

func TestWindowsPathChangeStartsOneshot(t *testing.T) {
	watchDir := t.TempDir()
	dir := t.TempDir()
	count := filepath.Join(dir, "count.txt")
	m := windowsPathManager(t, dir, watchDir, `
Type=oneshot
`, "exit", 0, count)
	if _, err := m.Start(context.Background(), "foo.path"); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "foo.path", core.Active)
	if err := os.WriteFile(filepath.Join(watchDir, "a.txt"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
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
	if err := os.WriteFile(filepath.Join(watchDir, "b.txt"), []byte("2"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitWindowsCount(t, count, 2, 8*time.Second)
}

func TestWindowsPathDoesNotRestartRunningSimple(t *testing.T) {
	watchDir := t.TempDir()
	dir := t.TempDir()
	m := windowsPathManager(t, dir, watchDir, `
Type=simple
`, "sleep", 0, "")
	if _, err := m.Start(context.Background(), "foo.service"); err != nil {
		t.Fatal(err)
	}
	proc := waitWindowsLiveProc(t, m, "foo.service")
	pid := proc.PID()
	gen := genOf(t, m, "foo.service")
	if _, err := m.Start(context.Background(), "foo.path"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(watchDir, "a.txt"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(800 * time.Millisecond)
	got := waitWindowsLiveProc(t, m, "foo.service")
	if got.PID() != pid {
		t.Fatalf("running simple restarted; pid %d -> %d", pid, got.PID())
	}
	if gotGen := genOf(t, m, "foo.service"); gotGen != gen {
		t.Fatalf("gen = %d after path change, want %d (C1 must not bump)", gotGen, gen)
	}
}

func TestWindowsPathMissingPathFailsConfiguration(t *testing.T) {
	watchDir := filepath.Join(t.TempDir(), "no-such-dir")
	dir := t.TempDir()
	m := windowsPathManager(t, dir, watchDir, `
Type=oneshot
`, "exit", 0, "")
	_, err := m.Start(context.Background(), "foo.path")
	if err == nil {
		t.Fatal("missing path must fail")
	}
	assertState(t, m, "foo.path", core.Failed)
	st, err := m.Status("foo.path")
	if err != nil || st.Unit == nil || st.Unit.Reason != core.ReasonConfiguration {
		t.Fatalf("status = %+v err=%v", st, err)
	}
	ms, err := m.Status("")
	if err != nil || ms.Machine == nil || ms.Machine.State != "running" {
		t.Fatalf("daemon must stay up: %+v", ms)
	}
}

func TestWindowsDisablePathDoesNotStartOneshot(t *testing.T) {
	watchDir := t.TempDir()
	dir := t.TempDir()
	count := filepath.Join(dir, "count.txt")
	m := windowsPathManager(t, dir, watchDir, `
Type=oneshot
`, "exit", 0, count)
	if _, err := m.Enable("foo.path"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Disable("foo.path"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "foo.path", core.Inactive)
	if err := os.WriteFile(filepath.Join(watchDir, "a.txt"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(800 * time.Millisecond)
	if helperCountLines(count) != 0 {
		t.Fatal("disabled path unit must not start the oneshot")
	}
}

func TestWindowsPathSubdirDoesNotFire(t *testing.T) {
	watchDir := t.TempDir()
	sub := filepath.Join(watchDir, "nested")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	count := filepath.Join(dir, "count.txt")
	m := windowsPathManager(t, dir, watchDir, `
Type=oneshot
`, "exit", 0, count)
	if _, err := m.Start(context.Background(), "foo.path"); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "foo.path", core.Active)
	if err := os.WriteFile(filepath.Join(sub, "deep.txt"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(800 * time.Millisecond)
	if helperCountLines(count) != 0 {
		t.Fatal("non-recursive watch must not fire on subdirectory writes")
	}
	if err := os.WriteFile(filepath.Join(watchDir, "top.txt"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitWindowsCount(t, count, 1, 8*time.Second)
}

func TestWindowsPathFileFilter(t *testing.T) {
	watchDir := t.TempDir()
	watched := filepath.Join(watchDir, "watched.txt")
	other := filepath.Join(watchDir, "other.txt")
	if err := os.WriteFile(watched, []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	count := filepath.Join(dir, "count.txt")
	m := windowsPathFileManager(t, dir, watched, `
Type=oneshot
`, "exit", 0, count)
	if _, err := m.Start(context.Background(), "foo.path"); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "foo.path", core.Active)
	if err := os.WriteFile(other, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(800 * time.Millisecond)
	if helperCountLines(count) != 0 {
		t.Fatal("sibling write must not start the oneshot")
	}
	if err := os.WriteFile(watched, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitWindowsCount(t, count, 1, 8*time.Second)
}

func windowsPathManager(t *testing.T, dir, watchPath, serviceBody, helper string, exit int, countPath string) *Manager {
	t.Helper()
	return windowsPathManagerAt(t, dir, watchPath, serviceBody, helper, exit, countPath)
}

func windowsPathFileManager(t *testing.T, dir, watchPath, serviceBody, helper string, exit int, countPath string) *Manager {
	t.Helper()
	return windowsPathManagerAt(t, dir, watchPath, serviceBody, helper, exit, countPath)
}

func windowsPathManagerAt(t *testing.T, dir, watchPath, serviceBody, helper string, exit int, countPath string) *Manager {
	t.Helper()
	return windowsPathManagerKeyed(t, dir, watchPath, "PathChanged", serviceBody, helper, exit, countPath)
}

func windowsPathExistsManager(t *testing.T, dir, watchPath, serviceBody, helper string, exit int, countPath string) *Manager {
	t.Helper()
	return windowsPathManagerKeyed(t, dir, watchPath, "PathExists", serviceBody, helper, exit, countPath)
}

func windowsPathManagerKeyed(t *testing.T, dir, watchPath, pathKey, serviceBody, helper string, exit int, countPath string) *Manager {
	t.Helper()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	exe, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	writeWindowsPathPair(t, units, dir, exe, pathKey, watchPath, serviceBody, helper, exit, countPath)
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

func writeWindowsPathPair(t *testing.T, units, wd, exe, pathKey, watchPath, serviceBody, helper string, exit int, countPath string) {
	t.Helper()
	if _, err := pathwatch.Parse(watchPath); err != nil {
		t.Fatalf("watch path %q: %v", watchPath, err)
	}
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
	pth := fmt.Sprintf(""+
		"[Path]\n"+
		"%s=%s\n"+
		"[Install]\n"+
		"WantedBy=default.target\n",
		pathKey, watchPath)
	if err := os.WriteFile(filepath.Join(units, "foo.path"), []byte(pth), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsPathExistsCreateSatisfies(t *testing.T) {
	watchDir := t.TempDir()
	target := filepath.Join(watchDir, "ready.flag")
	dir := t.TempDir()
	count := filepath.Join(dir, "count.txt")
	m := windowsPathExistsManager(t, dir, target, `
Type=oneshot
`, "exit", 0, count)
	if _, err := m.Start(context.Background(), "foo.path"); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "foo.path", core.Active)
	if helperCountLines(count) != 0 {
		t.Fatal("missing PathExists must not start the oneshot")
	}
	if err := os.WriteFile(target, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitWindowsCount(t, count, 1, 8*time.Second)
}

func TestWindowsPathExistsAlreadyPresentStartsOnce(t *testing.T) {
	watchDir := t.TempDir()
	target := filepath.Join(watchDir, "ready.flag")
	if err := os.WriteFile(target, []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	count := filepath.Join(dir, "count.txt")
	m := windowsPathExistsManager(t, dir, target, `
Type=oneshot
`, "exit", 0, count)
	if _, err := m.Start(context.Background(), "foo.path"); err != nil {
		t.Fatal(err)
	}
	waitWindowsCount(t, count, 1, 8*time.Second)
}

func TestWindowsPathExistsDeletionDoesNotStopSimple(t *testing.T) {
	watchDir := t.TempDir()
	target := filepath.Join(watchDir, "ready.flag")
	if err := os.WriteFile(target, []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	m := windowsPathExistsManager(t, dir, target, `
Type=simple
`, "sleep", 0, "")
	if _, err := m.Start(context.Background(), "foo.path"); err != nil {
		t.Fatal(err)
	}
	proc := waitWindowsLiveProc(t, m, "foo.service")
	pid := proc.PID()
	gen := genOf(t, m, "foo.service")
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	time.Sleep(800 * time.Millisecond)
	got := waitWindowsLiveProc(t, m, "foo.service")
	if got.PID() != pid {
		t.Fatalf("deletion must not stop the counterpart; pid %d -> %d", pid, got.PID())
	}
	if gotGen := genOf(t, m, "foo.service"); gotGen != gen {
		t.Fatalf("gen = %d after PathExists delete, want %d", gotGen, gen)
	}
}
