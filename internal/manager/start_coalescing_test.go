package manager

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/protocol"
)

func TestCompatibleStartJoinsAdmittedOperation(t *testing.T) {
	backend := newFakeSCM("example-worker")
	adapter := &pausedReloadSCM{SCM: backend, entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(adapter.release) }) }
	defer release()
	m := managerWithSCM(t, &fakeLauncher{}, adapter, map[string]string{
		"work.service": "[Service]\nType=scm\nServiceName=example-worker\n",
	})
	m.cfg.MaxStartTransactions = 1
	type reply struct {
		result *protocol.UnitResult
		err    error
	}
	first := make(chan reply, 1)
	go func() { r, err := m.Start(context.Background(), "work"); first <- reply{r, err} }()
	select {
	case <-adapter.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first start not admitted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := m.Start(ctx, "work")
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.OperationID == "" || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("compatible start did not join: %v", err)
	}
	op, err := m.Operation(pe.OperationID)
	if err != nil || op.State != "running" {
		t.Fatal("cancelled wait cancelled admitted operation")
	}
	release()
	r := <-first
	if r.err != nil || r.result.OperationID != pe.OperationID || backend.nstarts() != 1 {
		t.Fatal("joined start lost shared successful operation")
	}
}

type observedWaitContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *observedWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

func TestJoinedStartsReturnIndependentCopiesOfOneResult(t *testing.T) {
	adapter := &pausedReloadSCM{SCM: newFakeSCM("example-worker"), entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(adapter.release) }) }
	defer release()
	m := managerWithSCM(t, &fakeLauncher{}, adapter, map[string]string{
		"work.service": "[Service]\nType=scm\nServiceName=example-worker\n",
	})
	type reply struct {
		result *protocol.UnitResult
		err    error
	}
	first, second := make(chan reply, 1), make(chan reply, 1)
	go func() { r, err := m.Start(context.Background(), "work"); first <- reply{r, err} }()
	select {
	case <-adapter.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first start not admitted")
	}
	ctx := &observedWaitContext{Context: context.Background(), entered: make(chan struct{})}
	go func() { r, err := m.Start(ctx, "work"); second <- reply{r, err} }()
	select {
	case <-ctx.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("second caller did not join wait")
	}
	release()
	a, b := <-first, <-second
	if a.err != nil || b.err != nil || a.result.OperationID != b.result.OperationID {
		t.Fatal("compatible starts did not share outcome")
	}
	a.result.Unit = "changed by caller"
	if b.result.Unit != "work.service" {
		t.Fatal("joined callers share mutable result")
	}
}

func TestReloadMakesPendingStartIncompatible(t *testing.T) {
	testChangedIntentMakesPendingStartIncompatible(t, false)
}

func TestStopMakesPendingStartIncompatible(t *testing.T) {
	testChangedIntentMakesPendingStartIncompatible(t, true)
}

func testChangedIntentMakesPendingStartIncompatible(t *testing.T, stop bool) {
	t.Helper()
	adapter := &pausedReloadSCM{SCM: newFakeSCM("example-old", "example-new"), entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(adapter.release) }) }
	defer release()
	m := managerWithSCM(t, &fakeLauncher{}, adapter, map[string]string{
		"work.service": "[Service]\nType=scm\nServiceName=example-old\n",
	})
	m.cfg.MaxStartTransactions = 1
	done := make(chan error, 1)
	go func() { _, err := m.Start(context.Background(), "work"); done <- err }()
	select {
	case <-adapter.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first start not admitted")
	}
	var stopped chan error
	if stop {
		stopped = make(chan error, 1)
		go func() { _, err := m.Stop("work"); stopped <- err }()
		waitCond(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.units["work.service"].stopEpoch > 0 })
	} else {
		writeUnit(t, m.cfg.UnitsDir(), "work.service", "[Service]\nType=scm\nServiceName=example-new\n")
		if _, err := m.Reload(); err != nil {
			t.Fatal(err)
		}
	}
	_, err := m.Start(context.Background(), "work")
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.OperationID != "" || !strings.Contains(err.Error(), "capacity exhausted") {
		t.Fatal("new revision incorrectly joined old operation")
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if stopped != nil {
		if err := <-stopped; err != nil {
			t.Fatal(err)
		}
	}
}
