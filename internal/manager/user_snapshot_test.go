package manager

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
)

func snapshotControlClient(t *testing.T, c *Control) *protocol.Client {
	t.Helper()
	left, right := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); defer right.Close(); protocol.ServeConn(ctx, right, c, protocol.AllowAdmin) }()
	t.Cleanup(func() { cancel(); left.Close(); <-done })
	return protocol.NewClient(left)
}

func userSnapshotFromWire(t *testing.T, client *protocol.Client) *protocol.SnapshotResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s, err := client.Snapshot(ctx)
	if err != nil || s.UserHost == nil {
		t.Fatalf("system snapshot missing user ownership: %+v %v", s, err)
	}
	return s
}

func TestSystemSnapshotKeepsPendingSessionAndDoesNotQueryProcess(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	proc := &statusBlockedUser{release: make(chan struct{})}
	proc.sid = testSIDA
	proc.alive.Store(true)
	defer close(proc.release)
	h := NewUserHost(UserHostConfig{
		Admission: UserAdmission{Mode: "unit-files"}, ProbeUserUnits: func(*runtime.UserToken) (bool, error) { return true, nil },
		QueryToken: func(session uint32) (*runtime.UserToken, error) {
			close(entered)
			<-release
			return &runtime.UserToken{Info: runtime.UserInfo{SID: testSIDA}, SessionID: session}, nil
		},
		Start: func(runtime.UserManagerSpec) (runtime.UserManagerProc, error) { return proc, nil },
	})
	t.Cleanup(h.Close)
	client := snapshotControlClient(t, &Control{Units: testManager(t, nil), Users: h})
	done := make(chan struct{})
	go func() { h.Logon(7); close(done) }()
	<-entered
	before := userSnapshotFromWire(t, client)
	b := before.UserHost
	if b.HostID == "" || b.NativeWork != 1 || len(b.Sessions) != 1 || b.Sessions[0].PendingRequest == 0 || b.Sessions[0].SID != "" || len(b.Instances) != 0 {
		t.Fatalf("pending admission lost: %+v", b)
	}
	unblock()
	<-done
	after := userSnapshotFromWire(t, client)
	a := after.UserHost
	if before.ManagerID != after.ManagerID || after.Sequence <= before.Sequence || a.HostID != b.HostID || a.NativeWork != 0 || len(a.Instances) != 1 || len(a.Sessions) != 1 || a.Sessions[0].SID != testSIDA || a.Sessions[0].PendingRequest != 0 {
		t.Fatalf("admission completion lost identity: %+v", a)
	}
	instance := a.Instances[0]
	if after.Machine.UserManagers != 1 || after.Machine.UserNativeWork != a.NativeWork || instance.InstanceID == "" || instance.State != "running" || instance.PID != 1 || instance.SessionID != 7 || instance.InteractiveSessions != 1 {
		t.Fatalf("wrong accepted instance: %+v", instance)
	}
	a.Instances[0].Mode = "mutated"
	a.Sessions[0].SID = "mutated"
	fresh := userSnapshotFromWire(t, client).UserHost
	if fresh.Instances[0].Mode != "interactive" || fresh.Sessions[0].SID != testSIDA || len(b.Instances) != 0 || b.Sessions[0].PendingRequest == 0 {
		t.Fatal("published snapshot aliases another decision")
	}
}

func TestSystemSnapshotRetainsCleanupAndReplacementIdentity(t *testing.T) {
	p := &failingUserMgr{}
	p.sid = testSIDA
	p.alive.Store(true)
	p.fail.Store(true)
	h := NewUserHost(UserHostConfig{
		Admission: UserAdmission{Mode: "unit-files"}, ProbeUserUnits: func(*runtime.UserToken) (bool, error) { return true, nil },
		QueryToken: func(id uint32) (*runtime.UserToken, error) {
			return &runtime.UserToken{Info: runtime.UserInfo{SID: testSIDA}, SessionID: id}, nil
		},
		Start: func(runtime.UserManagerSpec) (runtime.UserManagerProc, error) { p.alive.Store(true); return p, nil },
	})
	defer h.Close()
	defer p.fail.Store(false)
	client := snapshotControlClient(t, &Control{Units: testManager(t, nil), Users: h})
	h.Logon(1)
	first := userSnapshotFromWire(t, client).UserHost
	h.Logoff(1)
	cleanup := userSnapshotFromWire(t, client).UserHost
	if len(cleanup.Instances) != 1 || len(cleanup.Recovery) != 1 || cleanup.Instances[0].State != "cleanup" || cleanup.Instances[0].PID != first.Instances[0].PID || cleanup.Instances[0].InstanceID != first.Instances[0].InstanceID || cleanup.Recovery[0].InstanceID != first.Instances[0].InstanceID || cleanup.Recovery[0].Error == "" || len(cleanup.Sessions) != 0 {
		t.Fatalf("cleanup ownership lost: %+v", cleanup)
	}
	p.fail.Store(false)
	if err := h.SetUserAdmission(UserAdmission{}); err != nil {
		t.Fatal(err)
	}
	empty := userSnapshotFromWire(t, client).UserHost
	if len(empty.Instances) != 0 || empty.AdmissionRevision <= first.AdmissionRevision {
		t.Fatal("revocation cleanup/revision missing")
	}
	if err := h.SetUserAdmission(UserAdmission{Mode: "unit-files"}); err != nil {
		t.Fatal(err)
	}
	h.Logon(2)
	next := userSnapshotFromWire(t, client).UserHost
	if len(next.Instances) != 1 || next.Instances[0].InstanceID == first.Instances[0].InstanceID || next.Instances[0].AdmissionRevision != next.AdmissionRevision || cleanup.Instances[0].State != "cleanup" {
		t.Fatal("replacement reused or relabelled retained identity")
	}
}

func TestSystemSnapshotBoundsIncludeUserDiagnostics(t *testing.T) {
	h := NewUserHost(UserHostConfig{})
	h.bySID[testSIDA] = &userInstance{sid: testSIDA, err: strings.Repeat("x", maxSnapshotBytes)}
	client := snapshotControlClient(t, &Control{Units: testManager(t, nil), Users: h})
	got, err := client.Snapshot(context.Background())
	var pe *protocol.Error
	if got != nil || !errors.As(err, &pe) || pe.Code != protocol.CodeFailed || !strings.Contains(pe.Message, "512 KiB") {
		t.Fatalf("oversized user diagnostics escaped bound: %+v %v", got, err)
	}
}
