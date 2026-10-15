//go:build windows

package manager

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/unit"
	"golang.org/x/sys/windows"
)

func TestUnitOfProcessFindsMainAndChild(t *testing.T) {
	dir := t.TempDir()
	m := startWindowsHelperUnit(t, dir, "tree.service", "Type=simple\n", "spawn", 0, "")
	if _, err := m.Start(context.Background(), "tree"); err != nil {
		t.Fatal(err)
	}
	proc := waitWindowsLiveProc(t, m, "tree.service")
	main := proc.PID()
	child := 0
	deadline := time.Now().Add(10 * time.Second)
	for child == 0 && time.Now().Before(deadline) {
		pids, err := proc.Job().PIDs()
		if err != nil {
			t.Fatal(err)
		}
		for _, pid := range pids {
			if pid != main {
				child = pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if child == 0 {
		t.Fatal("helper child did not appear in the unit job")
	}
	st, err := m.Status("tree")
	if err != nil {
		t.Fatal(err)
	}
	query := func(pid int) (*protocol.UnitOfProcessResult, error) {
		raw, err := json.Marshal(protocol.UnitOfProcessParams{PID: pid})
		if err != nil {
			t.Fatal(err)
		}
		got, err := m.Handle(context.Background(), protocol.MethodUnitOfProcess, raw)
		if err != nil {
			return nil, err
		}
		return got.(*protocol.UnitOfProcessResult), nil
	}
	for _, tc := range []struct {
		pid  int
		main bool
	}{{main, true}, {child, false}} {
		got, err := query(tc.pid)
		if err != nil {
			t.Fatalf("pid %d: %v", tc.pid, err)
		}
		if got.PID != tc.pid || got.Unit != "tree.service" || got.Main != tc.main || got.StopHelper || got.Scope != "system" || got.SID != "" {
			t.Fatalf("pid %d: %+v", tc.pid, got)
		}
		if got.InvocationID == "" || got.InvocationID != st.Unit.InvocationID {
			t.Fatalf("pid %d invocation %q, status %q", tc.pid, got.InvocationID, st.Unit.InvocationID)
		}
		if want := processCreatedAt(t, tc.pid); got.ProcessCreatedAt != want {
			t.Fatalf("pid %d created %s, own handle says %s", tc.pid, got.ProcessCreatedAt, want)
		}
	}
	var rpcErr *protocol.Error
	if _, err := query(os.Getpid()); !errors.As(err, &rpcErr) || rpcErr.Code != protocol.CodeNotFound {
		t.Fatalf("test process outside every unit: %v", err)
	}
	if _, err := m.Stop("tree"); err != nil {
		t.Fatal(err)
	}
	if _, err := query(child); !errors.As(err, &rpcErr) || rpcErr.Code != protocol.CodeNotFound {
		t.Fatalf("child after stop: %v", err)
	}
}

func processCreatedAt(t *testing.T, pid int) string {
	t.Helper()
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		t.Fatal(err)
	}
	return time.Unix(0, created.Nanoseconds()).UTC().Format(time.RFC3339Nano)
}

// heldLaunchLauncher holds a launch's return after the real launcher has resumed
// the process, the window in which the manager has not yet published it.
type heldLaunchLauncher struct {
	runtime.Launcher
	gate    func(runtime.StartSpec) bool
	started chan int
	release chan struct{}
}

func (g *heldLaunchLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	p, err := g.Launcher.Start(ctx, spec)
	if p != nil && g.gate(spec) {
		g.started <- p.PID()
		<-g.release
	}
	return p, err
}

func TestUnitOfProcessBeforeLaunchReturns(t *testing.T) {
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	exe, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	argv := func(mode string) string {
		b, err := json.Marshal([]string{exe, winunitdHelperArgPrefix + mode})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	body := "[Service]\nType=simple\nExecStart=" + argv("spawn") + "\nExecStop=" + argv("notify-never") +
		"\nTimeoutStopSec=2s\nWorkingDirectory=" + dir + "\n"
	if err := os.WriteFile(filepath.Join(units, "tree.service"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	g := &heldLaunchLauncher{Launcher: runtime.DefaultLauncher(), started: make(chan int, 1), release: make(chan struct{})}
	m, err := New(Config{BaseDir: dir, Launch: g})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = m.Stop("tree")
		m.Close()
	})
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	held := func(what string, gate func(runtime.StartSpec) bool, act func() error) (int, chan error) {
		t.Helper()
		g.gate = gate
		g.release = make(chan struct{})
		done := make(chan error, 1)
		go func() { done <- act() }()
		select {
		case pid := <-g.started:
			return pid, done
		case err := <-done:
			t.Fatalf("%s finished without launching: %v", what, err)
		case <-time.After(10 * time.Second):
			t.Fatalf("%s did not launch", what)
		}
		return 0, nil
	}

	pid, done := held("start", func(s runtime.StartSpec) bool { return s.Type == unit.TypeSimple }, func() error {
		_, err := m.Start(context.Background(), "tree")
		return err
	})
	got, err := m.UnitOfProcess(pid)
	close(g.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err != nil || got.Unit != "tree.service" || !got.Main || got.StopHelper || got.InvocationID == "" {
		t.Fatalf("main process before its launch returned: %+v %v", got, err)
	}

	pid, done = held("stop helper", func(s runtime.StartSpec) bool { return s.Type == unit.TypeOneshot }, func() error {
		_, err := m.Stop("tree")
		return err
	})
	got, err = m.UnitOfProcess(pid)
	close(g.release)
	<-done // the helper never exits; the stop deadline ends it
	if err != nil || got.Unit != "tree.service" || got.Main || !got.StopHelper {
		t.Fatalf("stop helper before its launch returned: %+v %v", got, err)
	}
}
