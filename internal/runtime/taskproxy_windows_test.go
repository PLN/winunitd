//go:build windows

package runtime

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestTaskProxyThrowawayTask(t *testing.T) {
	name := installThrowawayTask(t)
	ts := DefaultTaskScheduler()
	ctx := context.Background()

	st, err := ts.Start(ctx, name, 15*time.Second)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if st.ActiveState() != "active" {
		t.Fatalf("after start: %+v", st)
	}

	again, err := ts.Start(ctx, name, 15*time.Second)
	if err != nil {
		t.Fatalf("already-running start: %v", err)
	}
	if again.ActiveState() != "active" {
		t.Fatalf("already-running: %+v", again)
	}

	q, err := ts.Query(name)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if q.ActiveState() != "active" {
		t.Fatalf("query after start: %+v", q)
	}

	stopped, err := ts.Stop(ctx, name, 15*time.Second)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if stopped.ActiveState() != "inactive" {
		t.Fatalf("after stop: %+v", stopped)
	}

	againStop, err := ts.Stop(ctx, name, 15*time.Second)
	if err != nil {
		t.Fatalf("already-stopped stop: %v", err)
	}
	if againStop.ActiveState() != "inactive" {
		t.Fatalf("already-stopped: %+v", againStop)
	}
}

func TestTaskProxyMissingTask(t *testing.T) {
	_, err := DefaultTaskScheduler().Query(`\WinUnitdTS1\no-such-task-ts1`)
	if err == nil {
		t.Fatal("missing task must fail Query")
	}
	_, err = DefaultTaskScheduler().Start(context.Background(), `\WinUnitdTS1\no-such-task-ts1`, 5*time.Second)
	if err == nil {
		t.Fatal("missing task must fail Start")
	}
}

func installThrowawayTask(t *testing.T) string {
	t.Helper()
	exe, args := ThrowawayKeepAliveExec()
	folder := "WinUnitdTS1"
	name := fmt.Sprintf("wu-ts1-%d-%d", os.Getpid(), time.Now().UnixNano()%1e9)
	full, err := RegisterThrowawayTask(folder, name, exe, args)
	if err != nil {
		t.Skipf("Task Scheduler register %s: %v", full, err)
	}
	t.Cleanup(func() {
		ts := DefaultTaskScheduler()
		_, _ = ts.Stop(context.Background(), full, 10*time.Second)
		if err := DeleteThrowawayTask(folder, name); err != nil {
			t.Errorf("delete throwaway task %s: %v", full, err)
		}
	})
	return full
}
