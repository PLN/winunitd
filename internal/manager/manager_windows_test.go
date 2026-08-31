//go:build windows

package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"golang.org/x/sys/windows"
)

func TestMain(m *testing.M) {
	switch os.Getenv("WINUNITD_JOB_HELPER") {
	case "sleep":
		select {}
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
