package manager

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
)

type failedJournalStopLauncher struct {
	hangJournalLauncher
	proc *failedStopProcess
}

func (l *failedJournalStopLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	p, err := l.hangJournalLauncher.Start(ctx, spec)
	if err != nil {
		return nil, err
	}
	l.proc = &failedStopProcess{Process: p}
	l.proc.fail.Store(true)
	return l.proc, nil
}

func TestLateFailedStopKeepsOutcomeAfterSuccessfulRetry(t *testing.T) {
	launch := &failedJournalStopLauncher{}
	m := managerWith(t, launch, map[string]string{
		"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\nTimeoutStopSec=30s\n",
	})
	defer launch.closePipes()
	if _, err := m.Start(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	defer func() { launch.proc.fail.Store(false); _ = launch.proc.Process.Stop(time.Second) }()
	first := make(chan error, 1)
	go func() { _, err := m.Stop("work"); first <- err }()
	waitCond(t, func() bool {
		m.mu.Lock()
		uncertain := m.units["work.service"].cleanupPending()
		m.mu.Unlock()
		if !uncertain {
			return false
		}
		unlock, ok := m.ops.tryLock("work.service")
		if ok {
			unlock()
		}
		return ok
	})
	launch.proc.fail.Store(false)
	second := make(chan error, 1)
	go func() { _, err := m.Stop("work"); second <- err }()
	waitCond(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.units["work.service"].proc == nil
	})
	launch.closePipes()
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	firstErr := <-first
	if firstErr == nil {
		t.Fatal("late failed stop was reported as successful")
	}
	assertState(t, m, "work.service", core.Inactive)
	var pe *protocol.Error
	if !errors.As(firstErr, &pe) || pe.OperationID == "" {
		t.Fatal("failed stop lost history identity")
	}
	failed, err := m.Operation(pe.OperationID)
	if err != nil || failed.State != "failed" {
		t.Fatal("failed stop history changed after retry")
	}
	status, err := m.Status("work")
	if err != nil {
		t.Fatal(err)
	}
	retried, err := m.Operation(status.Unit.LastOperationID)
	if err != nil || retried.State != "succeeded" || retried.ID == failed.ID {
		t.Fatal("retry history lost independent outcome")
	}
}
