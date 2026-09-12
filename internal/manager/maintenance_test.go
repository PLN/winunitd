package manager

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
)

func maintenanceBlockedHost(t *testing.T) (*UserHost, func()) {
	t.Helper()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	h := NewUserHost(UserHostConfig{QueryToken: func(uint32) (*runtime.UserToken, error) {
		close(entered)
		<-release
		return nil, runtime.ErrNoUserToken
	}})
	go func() { h.Logon(1); close(done) }()
	awaitNativeWork(t, entered)
	return h, func() { unblock(); awaitNativeWork(t, done) }
}

func TestMaintenanceSurvivesCallerCancellationAndJoins(t *testing.T) {
	m := testManager(t, map[string]string{"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n"})
	if _, err := m.Start(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	h, release := maintenanceBlockedHost(t)
	c := &Control{Units: m, Users: h}
	caller, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := c.enterMaintenance(caller, protocol.MaintenanceParams{TimeoutMS: 2000}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("caller wait: %v", err)
	}
	first := c.maintenanceSnapshot()
	if first == nil || first.State != "quiescing" {
		t.Fatalf("maintenance lost accepted work: %+v", first)
	}
	if _, err := m.Start(context.Background(), "work"); err == nil {
		t.Error("maintenance admitted a new start")
	}
	joinCtx, joinCancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer joinCancel()
	if _, err := c.enterMaintenance(joinCtx, protocol.MaintenanceParams{TimeoutMS: 1}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("joined caller wait: %v", err)
	}
	if got := c.maintenanceSnapshot(); got.StartedAt != first.StartedAt || got.Deadline != first.Deadline || got.State != "quiescing" {
		t.Error("joining caller replaced the accepted deadline")
	}
	release()
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	result, err := c.enterMaintenance(ctx, protocol.MaintenanceParams{})
	if err != nil || result.State != "quiesced" || result.StartedAt != first.StartedAt {
		t.Fatalf("maintenance completion: %+v %v", result, err)
	}
	for _, verb := range []func(string) (*protocol.EnableResult, error){m.Enable, m.Disable} {
		if _, err := verb("work"); err == nil {
			t.Error("maintenance admitted configuration mutation")
		}
	}
	raw, _ := json.Marshal(protocol.StatusParams{})
	status, err := c.Handle(ctx, protocol.MethodStatus, raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := status.(*protocol.StatusResult).Machine.Maintenance; got == nil || got.State != "quiesced" {
		t.Fatal("diagnostics lost maintenance state")
	}
	if again, err := c.enterMaintenance(ctx, protocol.MaintenanceParams{TimeoutMS: 1}); err != nil || again.StartedAt != first.StartedAt {
		t.Fatal("completed maintenance was not idempotent")
	}
}

func TestMaintenanceDeadlineFailureRetainsWorkAndRetries(t *testing.T) {
	m := testManager(t, nil)
	h, release := maintenanceBlockedHost(t)
	c := &Control{Units: m, Users: h}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if result, err := c.enterMaintenance(ctx, protocol.MaintenanceParams{TimeoutMS: 20}); err == nil || result != nil {
		t.Fatal("maintenance reported success with pending native work")
	}
	first := c.maintenanceSnapshot()
	if first.State != "failed" || first.Error == "" || h.NativeWorkCount() != 1 {
		t.Fatal("failed maintenance lost diagnostics or ownership")
	}
	release()
	result, err := c.enterMaintenance(ctx, protocol.MaintenanceParams{TimeoutMS: 500})
	if err != nil || result.State != "quiesced" || result.StartedAt == first.StartedAt {
		t.Fatalf("retry: %+v %v", result, err)
	}
}

func TestMaintenanceRejectsInvalidOrCanceledAdmission(t *testing.T) {
	m := testManager(t, nil)
	c := &Control{Units: m}
	for _, value := range []int64{-1, protocol.MaxMaintenanceTimeoutMS + 1} {
		if _, err := c.enterMaintenance(context.Background(), protocol.MaintenanceParams{TimeoutMS: value}); err == nil {
			t.Error("invalid timeout accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.enterMaintenance(ctx, protocol.MaintenanceParams{}); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled admission accepted")
	}
	if c.maintenanceSnapshot() != nil {
		t.Fatal("rejected request changed maintenance state")
	}
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		t.Fatal("rejected request closed admission")
	}
	m.cfg.UserScope = true
	if _, err := c.enterMaintenance(context.Background(), protocol.MaintenanceParams{}); err == nil {
		t.Fatal("user manager accepted global maintenance")
	}
}
