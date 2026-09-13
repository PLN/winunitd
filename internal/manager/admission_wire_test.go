package manager

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
)

type admissionLateLauncher struct {
	fakeLauncher
	entered, release chan struct{}
}

func (l *admissionLateLauncher) Start(_ context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	close(l.entered)
	<-l.release // model an OS call that completes after cancellation
	return l.fakeLauncher.Start(context.Background(), spec)
}

func TestControlAdmissionPreservesSnapshotStopAndMaintenance(t *testing.T) {
	launch := &admissionLateLauncher{entered: make(chan struct{}), release: make(chan struct{})}
	var launchOnce sync.Once
	releaseLaunch := func() { launchOnce.Do(func() { close(launch.release) }) }
	defer releaseLaunch()
	m := managerWith(t, launch, map[string]string{
		"blocked.service": "[Service]\nExecStart=C:\\Tools\\blocked.exe\nTimeoutStartSec=15s\nTimeoutStopSec=15s\n",
		"other.target":    "[Unit]\nDescription=Independent\n",
	})
	if _, err := m.Start(context.Background(), "other.target"); err != nil {
		t.Fatal(err)
	}
	tokenEntered, tokenRelease, tokenDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var tokenOnce sync.Once
	releaseToken := func() { tokenOnce.Do(func() { close(tokenRelease) }) }
	defer releaseToken()
	h := NewUserHost(UserHostConfig{QueryToken: func(uint32) (*runtime.UserToken, error) {
		close(tokenEntered)
		<-tokenRelease
		return nil, runtime.ErrNoUserToken
	}})
	t.Cleanup(h.Close)
	go func() { h.Logon(1); close(tokenDone) }()
	awaitNativeWork(t, tokenEntered)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- protocol.ServeWithLimits(ctx, lis, &Control{Units: m, Users: h}, protocol.AllowAdmin, protocol.ServerLimits{Connections: 12, Requests: 1, Stops: 1, Diagnostics: 1})
	}()
	t.Cleanup(func() { cancel(); <-serverDone })
	call := func(method string, params, result any) error {
		conn, err := net.DialTimeout("tcp", lis.Addr().String(), 5*time.Second)
		if err != nil {
			return err
		}
		defer conn.Close()
		if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			return err
		}
		return protocol.NewClient(conn).Call(ctx, method, params, result)
	}
	snapshot := func() *protocol.SnapshotResult {
		t.Helper()
		var s protocol.SnapshotResult
		if err := call(protocol.MethodSnapshot, nil, &s); err != nil {
			t.Fatal("reserved snapshot failed", err)
		}
		if s.UserHost == nil || s.Machine.UserNativeWork != s.UserHost.NativeWork {
			t.Fatal("incoherent user ownership")
		}
		return &s
	}
	startDone := make(chan error, 1)
	go func() { startDone <- call(protocol.MethodStart, protocol.UnitParams{Unit: "blocked.service"}, nil) }()
	awaitNativeWork(t, launch.entered)
	before := snapshot()
	u := snapshotUnit(t, before, "blocked.service")
	if u.LastOperationID == "" || len(before.Operations) != 1 || before.Operations[0].ID != u.LastOperationID || before.UserHost.NativeWork != 1 {
		t.Fatal("pending ownership not published")
	}
	for i := 0; i < 20; i++ {
		var busy *protocol.Error
		err := call(protocol.MethodStart, protocol.UnitParams{Unit: "other.target"}, nil)
		if !errors.As(err, &busy) || busy.Code != protocol.CodeBusy {
			t.Fatalf("ordinary overload: %v", err)
		}
	}
	if err := call(protocol.MethodStop, protocol.UnitParams{Unit: "other.target"}, nil); err != nil {
		t.Fatal("reserved stop failed", err)
	}
	maintenanceDone := make(chan error, 1)
	var maintenance protocol.MaintenanceResult
	go func() {
		maintenanceDone <- call(protocol.MethodMaintenance, protocol.MaintenanceParams{TimeoutMS: 10000}, &maintenance)
	}()
	waitCond(t, func() bool { s := snapshot(); return s.Machine.State == "closing" && s.UserHost.State == "closing" })
	// Maintenance owns the only stop slot; diagnostics and completion must still
	// progress while both the launch and token observation remain outstanding.
	var busy *protocol.Error
	err = call(protocol.MethodStop, protocol.UnitParams{Unit: "other.target"}, nil)
	if !errors.As(err, &busy) || busy.Code != protocol.CodeBusy {
		t.Fatalf("stop saturation: %v", err)
	}
	during := snapshot()
	if during.UserHost.NativeWork != 1 {
		t.Fatal("maintenance discarded pending token")
	}
	releaseLaunch()
	if err := <-startDone; err == nil {
		t.Fatal("late canceled launch reported success")
	}
	waitCond(t, func() bool {
		s := snapshot()
		return len(s.Operations) == 0 && snapshotUnit(t, s, "blocked.service").MainPID == 0
	})
	if len(launch.stopped()) != 1 {
		t.Fatal("late successful creation was not cleaned up exactly once")
	}
	var op protocol.OperationResult
	if err := call(protocol.MethodOperation, protocol.OperationParams{ID: u.LastOperationID}, &op); err != nil || op.State == "running" || op.State == "succeeded" {
		t.Fatalf("terminal operation: %+v %v", op, err)
	}
	releaseToken()
	awaitNativeWork(t, tokenDone)
	if err := <-maintenanceDone; err != nil || maintenance.State != "quiesced" {
		t.Fatalf("maintenance completion: %+v %v", maintenance, err)
	}
	after := snapshot()
	if after.UserHost.NativeWork != 0 || len(after.UserHost.Instances) != 0 || after.Sequence <= before.Sequence || after.ManagerID != before.ManagerID {
		t.Fatal("completion lost coherent ownership")
	}
	if u.LastOperationID == "" || before.Operations[0].State != "running" || before.UserHost.NativeWork != 1 {
		t.Fatal("old snapshot changed after completion")
	}
}
