//go:build windows

package manager

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/protocol"
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
