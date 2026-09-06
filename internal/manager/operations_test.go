package manager

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
)

func TestOperationSurvivesClientDisconnectAndHistoryEviction(t *testing.T) {
	adapter := &pausedReloadSCM{SCM: newFakeSCM("example-worker"), entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(adapter.release) }) }
	defer release()
	m := managerWithSCM(t, &fakeLauncher{}, adapter, map[string]string{
		"work.service": "[Service]\nType=scm\nServiceName=example-worker\n",
		"app.target":   "[Unit]\nDescription=Application\n",
	})
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go protocol.Serve(ctx, lis, m, protocol.AllowAdmin)
	conn, err := net.DialTimeout("tcp", lis.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	clientDone := make(chan error, 1)
	go func() { _, err := protocol.NewClient(conn).Start(ctx, "work"); clientDone <- err }()
	select {
	case <-adapter.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("start not admitted")
	}
	st, err := m.Status("work")
	if err != nil {
		t.Fatal(err)
	}
	id := st.Unit.LastOperationID
	if id == "" {
		t.Fatal("pending operation has no identity")
	}
	_ = conn.Close()
	if err := <-clientDone; err == nil {
		t.Fatal("disconnected client received success")
	}
	op, err := m.Operation(id)
	if err != nil || op.State != "running" {
		t.Fatalf("pending operation: %+v %v", op, err)
	}
	op.State = "changed by caller"
	var first, last string
	for i := 0; i < completedOperationLimit+4; i++ {
		r, err := m.Start(ctx, "app.target")
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = r.OperationID
		}
		last = r.OperationID
	}
	if _, err := m.Operation(first); err == nil {
		t.Fatal("old completed operation not evicted")
	}
	if _, err := m.Operation(last); err != nil {
		t.Fatal(err)
	}
	op, err = m.Operation(id)
	if err != nil || op.State != "running" {
		t.Fatal("pending operation evicted or mutable")
	}
	m.mu.Lock()
	count := len(m.operations)
	m.mu.Unlock()
	if count != completedOperationLimit+1 {
		t.Fatalf("retained operation count = %d", count)
	}
	release()
	waitCond(t, func() bool { op, err := m.Operation(id); return err == nil && op.State == "succeeded" })
	reconnected, err := net.DialTimeout("tcp", lis.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer reconnected.Close()
	_ = reconnected.SetDeadline(time.Now().Add(2 * time.Second))
	op, err = protocol.NewClient(reconnected).Operation(ctx, id)
	if err != nil || op.State != "succeeded" || op.CompletedAt == "" {
		t.Fatalf("reconnected outcome: %+v %v", op, err)
	}
}

type longOperationErrorLauncher struct{}

func (longOperationErrorLauncher) Start(context.Context, runtime.StartSpec) (runtime.Process, error) {
	return nil, errors.New(strings.Repeat("failure 🧪 ", 1000))
}

func TestFailedOperationIdentityAndBoundedError(t *testing.T) {
	m := managerWith(t, longOperationErrorLauncher{}, map[string]string{
		"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n",
	})
	r, err := m.Start(context.Background(), "work")
	var pe *protocol.Error
	if !errors.As(err, &pe) || pe.OperationID == "" || r.OperationID != pe.OperationID {
		t.Fatal("failure lost operation ID")
	}
	op, err := m.Operation(pe.OperationID)
	if err != nil || op.State != "failed" || !op.ErrorTruncated || len(op.Error) > operationErrorLimit || !utf8.ValidString(op.Error) {
		t.Fatalf("invalid bounded failure: %v", err)
	}
	st, err := m.Status("work")
	if err != nil || st.Unit.LastOperationID != pe.OperationID {
		t.Fatal("status lost failed operation")
	}
	if _, err := m.Start(context.Background(), "missing"); err == nil {
		t.Fatal("invalid request accepted")
	}
	m.mu.Lock()
	count := len(m.operations)
	m.mu.Unlock()
	if count != 1 {
		t.Fatal("rejected request created operation")
	}
}

func TestStopOperationHasReservedBoundedAdmission(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	adapter := &blockedSCMStop{SCM: newFakeSCM("example-worker"), release: release}
	m := managerWithSCM(t, &fakeLauncher{}, adapter, map[string]string{
		"work.service": "[Service]\nType=scm\nServiceName=example-worker\nTimeoutStopSec=30s\n",
		"app.target":   "[Unit]\nDescription=Application\n",
	})
	m.cfg.MaxStopTransactions = 1
	if _, err := m.Start(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := m.Stop("work"); done <- err }()
	waitCond(t, func() bool { return adapter.calls.Load() == 1 })
	if _, err := m.Stop("work"); err == nil || !strings.Contains(err.Error(), "stop transaction capacity exhausted") {
		t.Fatalf("stop overload: %v", err)
	}
	if _, err := m.Start(context.Background(), "app.target"); err != nil {
		t.Fatal("stop saturation consumed start capacity", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := m.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown did not bypass admission: %v", err)
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	active := m.activeStops
	m.mu.Unlock()
	if active != 0 {
		t.Fatal("stop capacity leaked")
	}
}
