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
	winreg "github.com/PLN/winunitd/internal/registry"
	"github.com/PLN/winunitd/internal/unit"
	"golang.org/x/sys/windows/registry"
)

func TestWindowsRegistryChangeStartsOneshot(t *testing.T) {
	keyPath := testRegistryPath(t)
	createHKLMKey(t, keyPath)
	dir := t.TempDir()
	count := filepath.Join(dir, "count.txt")
	m := windowsRegistryManager(t, dir, keyPath, `
Type=oneshot
`, "exit", 0, count)
	if _, err := m.Start(context.Background(), "foo.registry"); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "foo.registry", core.Active)
	setHKLMValue(t, keyPath, "v", "1")
	waitWindowsCount(t, count, 1, 8*time.Second)
	waitUntil(t, 2*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		proc := m.procs["foo.service"]
		return proc == nil || !proc.Alive()
	})
	if _, err := m.Stop("foo.service"); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "foo.service", core.Inactive)
	setHKLMValue(t, keyPath, "v", "2")
	waitWindowsCount(t, count, 2, 8*time.Second)
}

func TestWindowsRegistryDoesNotRestartRunningSimple(t *testing.T) {
	keyPath := testRegistryPath(t)
	createHKLMKey(t, keyPath)
	dir := t.TempDir()
	m := windowsRegistryManager(t, dir, keyPath, `
Type=simple
`, "sleep", 0, "")
	if _, err := m.Start(context.Background(), "foo.service"); err != nil {
		t.Fatal(err)
	}
	proc := waitWindowsLiveProc(t, m, "foo.service")
	pid := proc.PID()
	if _, err := m.Start(context.Background(), "foo.registry"); err != nil {
		t.Fatal(err)
	}
	setHKLMValue(t, keyPath, "v", "1")
	time.Sleep(800 * time.Millisecond)
	got := waitWindowsLiveProc(t, m, "foo.service")
	if got.PID() != pid {
		t.Fatalf("running simple restarted; pid %d -> %d", pid, got.PID())
	}
}

func TestWindowsRegistryMissingKeyFailsConfiguration(t *testing.T) {
	keyPath := testRegistryPath(t)
	deleteHKLMKey(t, keyPath)
	dir := t.TempDir()
	m := windowsRegistryManager(t, dir, keyPath, `
Type=oneshot
`, "exit", 0, "")
	_, err := m.Start(context.Background(), "foo.registry")
	if err == nil {
		t.Fatal("missing key must fail")
	}
	assertState(t, m, "foo.registry", core.Failed)
	st, err := m.Status("foo.registry")
	if err != nil || st.Unit == nil || st.Unit.Reason != core.ReasonConfiguration {
		t.Fatalf("status = %+v err=%v", st, err)
	}
	ms, err := m.Status("")
	if err != nil || ms.Machine == nil || ms.Machine.State != "running" {
		t.Fatalf("daemon must stay up: %+v", ms)
	}
}

func TestWindowsRegistryDeletedKeyFailsConfiguration(t *testing.T) {
	keyPath := testRegistryPath(t)
	createHKLMKey(t, keyPath)
	dir := t.TempDir()
	m := windowsRegistryManager(t, dir, keyPath, `
Type=oneshot
`, "exit", 0, "")
	if _, err := m.Start(context.Background(), "foo.registry"); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "foo.registry", core.Active)
	deleteHKLMKey(t, keyPath)
	waitUntil(t, 8*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.stateOfLocked("foo.registry") == core.Failed
	})
	st, err := m.Status("foo.registry")
	if err != nil || st.Unit == nil || st.Unit.Reason != core.ReasonConfiguration {
		t.Fatalf("status = %+v err=%v", st, err)
	}
	ms, err := m.Status("")
	if err != nil || ms.Machine == nil || ms.Machine.State != "running" {
		t.Fatalf("daemon must stay up: %+v", ms)
	}
}

func TestWindowsDisableRegistryDoesNotStartOneshot(t *testing.T) {
	keyPath := testRegistryPath(t)
	createHKLMKey(t, keyPath)
	dir := t.TempDir()
	count := filepath.Join(dir, "count.txt")
	m := windowsRegistryManager(t, dir, keyPath, `
Type=oneshot
`, "exit", 0, count)
	if _, err := m.Enable("foo.registry"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Disable("foo.registry"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "foo.registry", core.Inactive)
	setHKLMValue(t, keyPath, "v", "1")
	time.Sleep(800 * time.Millisecond)
	if helperCountLines(count) != 0 {
		t.Fatal("disabled registry unit must not start the oneshot")
	}
}

func TestWindowsUserHKCUWatch(t *testing.T) {
	if err := winreg.UserHiveWatchOK(); err != nil {
		t.Skip(err.Error())
	}
	keyPath := `Software\winunitd\t1-registry\` + uniqueRegName(t)
	k, _, err := registry.CreateKey(registry.CURRENT_USER, keyPath, registry.ALL_ACCESS)
	if err != nil {
		t.Skipf("cannot open HKCU: %v", err)
	}
	_ = k.Close()
	t.Cleanup(func() {
		_ = registry.DeleteKey(registry.CURRENT_USER, keyPath)
	})

	dir := t.TempDir()
	count := filepath.Join(dir, "count.txt")
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	exe, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	writeWindowsRegistryPair(t, units, dir, exe, keyPath, "HKCU", `
Type=oneshot
`, "exit", 0, count)
	m, err := New(Config{BaseDir: dir, UserScope: true})
	if err != nil {
		t.Fatal(err)
	}
	rel, err := m.Reload()
	if err != nil {
		t.Fatal(err)
	}
	if len(rel.Errors) > 0 {
		t.Fatalf("reload errors: %v", rel.Errors)
	}
	t.Cleanup(func() { stopAll(m) })
	if _, err := m.Start(context.Background(), "foo.registry"); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "foo.registry", core.Active)
	setHiveValue(t, registry.CURRENT_USER, keyPath, "v", "1")
	waitWindowsCount(t, count, 1, 8*time.Second)
}

func windowsRegistryManager(t *testing.T, dir, keyPath, serviceBody, helper string, exit int, countPath string) *Manager {
	t.Helper()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	exe, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	writeWindowsRegistryPair(t, units, dir, exe, keyPath, "HKLM", serviceBody, helper, exit, countPath)
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

func writeWindowsRegistryPair(t *testing.T, units, wd, exe, keyPath, hive, serviceBody, helper string, exit int, countPath string) {
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
	reg := fmt.Sprintf(""+
		"[Registry]\n"+
		"RegistryChanged=%s\\%s\n"+
		"[Install]\n"+
		"WantedBy=default.target\n",
		hive, keyPath)
	if err := os.WriteFile(filepath.Join(units, "foo.registry"), []byte(reg), 0o644); err != nil {
		t.Fatal(err)
	}
}

func testRegistryPath(t *testing.T) string {
	t.Helper()
	return `Software\winunitd\t1-registry\` + uniqueRegName(t)
}

func uniqueRegName(t *testing.T) string {
	return fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
}

func createHKLMKey(t *testing.T, path string) {
	t.Helper()
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, path, registry.ALL_ACCESS)
	if err != nil {
		t.Skipf("cannot create HKLM\\%s (need admin): %v", path, err)
	}
	_ = k.Close()
	t.Cleanup(func() { deleteHKLMKey(t, path) })
}

func deleteHKLMKey(t *testing.T, path string) {
	t.Helper()
	_ = registry.DeleteKey(registry.LOCAL_MACHINE, path)
}

func setHKLMValue(t *testing.T, path, name, value string) {
	t.Helper()
	setHiveValue(t, registry.LOCAL_MACHINE, path, name, value)
}

func setHiveValue(t *testing.T, root registry.Key, path, name, value string) {
	t.Helper()
	k, err := registry.OpenKey(root, path, registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	if err := k.SetStringValue(name, value); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsSystemVerifyRejectsHKCU(t *testing.T) {
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
	writeUnit(t, units, "foo.registry", `
[Registry]
RegistryChanged=HKCU\Software\Example
`)
	rep := unit.VerifyPath(filepath.Join(units, "foo.registry"))
	issues := unit.RegistryScopeIssues(rep.Unit, false)
	if len(issues) == 0 {
		t.Fatal("system verify must reject HKCU")
	}
}
