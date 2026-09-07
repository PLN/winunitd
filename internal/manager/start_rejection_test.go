package manager

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/runtime"
)

type rejectedBranchLauncher struct {
	fakeLauncher
	entered chan struct{}
	release chan struct{}
}

func (l *rejectedBranchLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	switch spec.Unit {
	case "slow.service":
		close(l.entered)
		select {
		case <-l.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	case "db.service":
		select {
		case <-l.entered:
			return nil, errors.New("database start failed")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return l.fakeLauncher.Start(ctx, spec)
}

func TestRejectedMemberPublishesBeforeIndependentWorkerReturns(t *testing.T) {
	l := &rejectedBranchLauncher{entered: make(chan struct{}), release: make(chan struct{})}
	m := managerWith(t, l, map[string]string{
		"app.target":   "[Unit]\nRequires=db.service\nAfter=db.service\nWants=slow.service\n",
		"db.service":   "[Service]\nExecStart=C:\\Tools\\db.exe\n",
		"slow.service": "[Service]\nExecStart=C:\\Tools\\slow.exe\n",
	})
	var once sync.Once
	release := func() { once.Do(func() { close(l.release) }) }
	t.Cleanup(release)
	done := make(chan error, 1)
	go func() { _, err := m.Start(context.Background(), "app.target"); done <- err }()
	waitCond(t, func() bool {
		st, err := m.Status("app.target")
		return err == nil && st.Unit.ActiveState == "failed" && strings.Contains(st.Unit.Error, "db.service")
	})
	select {
	case <-done:
		t.Fatal("independent worker should still own the accepted operation")
	default:
	}
	// A later stop owns lifecycle state even though this transaction still runs.
	if _, err := m.Stop("app.target"); err != nil {
		t.Fatal(err)
	}
	release()
	if err := waitErr(t, done); err == nil {
		t.Fatal("dependency failure was lost from the operation outcome")
	}
	assertState(t, m, "app.target", core.Inactive)
	assertState(t, m, "slow.service", core.Active)
}

func TestRejectedMemberCannotOverwriteNewInvocation(t *testing.T) {
	m := managerWith(t, &fakeLauncher{}, map[string]string{"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n"})
	m.mu.Lock()
	rt := m.units["work.service"]
	old := &plannedStart{record: rt, gen: rt.gen, stopEpoch: rt.stopEpoch}
	m.mu.Unlock()
	if _, err := m.Start(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	m.applyStartRejection(startCompletion{name: "work.service", plan: old, err: errors.New("old dependency failed")})
	assertState(t, m, "work.service", core.Active)
	st, err := m.Status("work")
	if err != nil || st.Unit.Error != "" {
		t.Fatal("stale rejection replaced current diagnostics")
	}
}

func TestRejectedMemberAfterDeadlineKeepsFailureVisible(t *testing.T) {
	m, clock := managerWithFake(t, &fakeLauncher{}, map[string]string{"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n"})
	m.cfg.OperationTimeout = time.Second
	unlock := m.ops.lock("work.service")
	defer unlock()
	done := make(chan error, 1)
	go func() { _, err := m.Start(context.Background(), "work"); done <- err }()
	waitCond(t, func() bool { m.ops.mu.Lock(); defer m.ops.mu.Unlock(); return m.ops.by["work.service"].refs == 2 })
	clock.Advance(time.Second)
	waitOperationErrorCompleted(t, m, waitErr(t, done))
	assertState(t, m, "work.service", core.Failed)
}
