package manager

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/protocol"
)

func TestCancelOperationPreservesCompletedDependency(t *testing.T) {
	launch := &dependencyOperationLauncher{entered: make(chan struct{}), release: make(chan struct{})}
	m := managerWith(t, launch, map[string]string{
		"ready.service": "[Service]\nExecStart=C:\\Tools\\ready.exe\n",
		"slow.service":  "[Unit]\nRequires=ready.service\nAfter=ready.service\n[Service]\nExecStart=C:\\Tools\\slow.exe\n",
	})
	var once sync.Once
	release := func() { once.Do(func() { close(launch.release) }) }
	defer release()
	done := make(chan error, 1)
	go func() { _, err := m.Start(context.Background(), "slow"); done <- err }()
	select {
	case <-launch.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("launch did not enter")
	}
	status, _ := m.Status("slow")
	id := status.Unit.LastOperationID
	joinedCtx := &observedWaitContext{Context: context.Background(), entered: make(chan struct{})}
	joined := make(chan error, 1)
	go func() { _, err := m.Start(joinedCtx, "slow"); joined <- err }()
	select {
	case <-joinedCtx.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("second caller did not join")
	}
	client, stop := serveManager(t, m, protocol.AllowAdmin)
	defer stop()
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.CancelOperation(canceledCtx, id); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		op, err := client.CancelOperation(context.Background(), id)
		if err != nil || op.State != "running" || op.CancellationReason == "" {
			t.Fatalf("cancel response: %+v %v", op, err)
		}
		op.CancellationReason = "mutated reply"
	}
	err := waitErr(t, done)
	joinedErr := waitErr(t, joined)
	var joinedRPC *protocol.Error
	if !errors.As(joinedErr, &joinedRPC) || joinedRPC.OperationID != id {
		t.Fatalf("joined cancellation: %v", joinedErr)
	}
	if err == nil {
		t.Fatal("canceled start succeeded")
	}
	assertState(t, m, "ready.service", core.Active)
	release()
	op := waitOperationErrorCompleted(t, m, err)
	if op.State != "failed" || op.CancellationReason == "mutated reply" {
		t.Fatalf("retained result: %+v", op)
	}
	assertState(t, m, "ready.service", core.Active)
	m.mu.Lock()
	late := m.units["slow.service"].proc
	m.mu.Unlock()
	if late != nil {
		t.Fatal("late creation was not cleaned up")
	}
	repeated, err := client.CancelOperation(context.Background(), id)
	if err != nil || *repeated != *op {
		t.Fatalf("completed retry: %+v %v", repeated, err)
	}
}

func TestCancelOperationBeforeAdapterAdmission(t *testing.T) {
	launch := &fakeLauncher{}
	m := managerWith(t, launch, map[string]string{"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n"})
	unlock := m.ops.lock("work.service")
	defer unlock()
	done := make(chan error, 1)
	go func() { _, err := m.Start(context.Background(), "work"); done <- err }()
	var id string
	waitCond(t, func() bool { st, _ := m.Status("work"); id = st.Unit.LastOperationID; return id != "" })
	if _, err := m.CancelOperation(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	waitOperationErrorCompleted(t, m, waitErr(t, done))
	if len(launch.units()) != 0 {
		t.Fatal("canceled queued operation launched a process")
	}
}

func TestCancelOperationRetainsAcceptedStop(t *testing.T) {
	launch := &operationStopLauncher{release: make(chan struct{})}
	m := managerWith(t, launch, map[string]string{"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\nTimeoutStopSec=30s\n"})
	var once sync.Once
	release := func() { once.Do(func() { close(launch.release) }) }
	defer release()
	if _, err := m.Start(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := m.Stop("work"); done <- err }()
	waitCond(t, func() bool { return launch.proc.calls.Load() == 1 })
	st, _ := m.Status("work")
	id := st.Unit.LastOperationID
	if _, err := m.CancelOperation(context.Background(), id); err == nil {
		t.Fatal("accepted stop cancellation allowed")
	}
	op, _ := m.Operation(id)
	if op.CancellationReason != "" || op.State != "running" {
		t.Fatalf("stop changed: %+v", op)
	}
	release()
	if err := waitErr(t, done); err != nil {
		t.Fatal(err)
	}
	op, err := m.CancelOperation(context.Background(), id)
	if err != nil || op.State != "succeeded" {
		t.Fatalf("completed stop: %+v %v", op, err)
	}
}

func TestCancelRestartDuringStopDoesNotRelaunch(t *testing.T) {
	launch := &operationStopLauncher{release: make(chan struct{})}
	m := managerWith(t, launch, map[string]string{"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\nTimeoutStopSec=30s\n"})
	var once sync.Once
	release := func() { once.Do(func() { close(launch.release) }) }
	defer release()
	if _, err := m.Start(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := m.Restart(context.Background(), "work"); done <- err }()
	waitCond(t, func() bool { return launch.proc.calls.Load() == 1 })
	st, _ := m.Status("work")
	if _, err := m.CancelOperation(context.Background(), st.Unit.LastOperationID); err != nil {
		t.Fatal(err)
	}
	waitOperationErrorCompleted(t, m, waitErr(t, done))
	if len(launch.units()) != 1 {
		t.Fatal("canceled restart relaunched")
	}
	release()
	if _, err := m.Stop("work"); err != nil {
		t.Fatal("retained cleanup retry", err)
	}
	if launch.proc.Alive() {
		t.Fatal("old invocation survived cleanup")
	}
}

func TestCancelOperationValidationAndCompletedResult(t *testing.T) {
	m := testManager(t, map[string]string{})
	for _, tc := range []struct{ id, code string }{{"", protocol.CodeInvalidParams}, {" bad ", protocol.CodeInvalidParams}, {"missing", protocol.CodeNotFound}} {
		_, err := m.CancelOperation(context.Background(), tc.id)
		var rpcErr *protocol.Error
		if !errors.As(err, &rpcErr) || rpcErr.Code != tc.code {
			t.Fatalf("%q: %v", tc.id, err)
		}
	}
	result, err := m.Start(context.Background(), DefaultTarget)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := m.Operation(result.OperationID)
	after, err := m.CancelOperation(context.Background(), result.OperationID)
	if err != nil || *before != *after {
		t.Fatalf("completed operation changed: %+v %v", after, err)
	}
}
