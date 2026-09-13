package manager

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/protocol"
)

func TestSnapshotKeepsAcceptedOperationAndUnitTogether(t *testing.T) {
	l := &lateOperationLauncher{entered: make(chan struct{}), release: make(chan struct{})}
	m := managerWith(t, l, map[string]string{"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n"})
	var once sync.Once
	release := func() { once.Do(func() { close(l.release) }) }
	defer release()
	done := make(chan error, 1)
	go func() { _, err := m.Start(context.Background(), "work"); done <- err }()
	select {
	case <-l.entered:
	case <-time.After(time.Second):
		t.Fatal("launch did not enter")
	}
	snapshot, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Operations) != 1 || len(snapshot.Units) != snapshot.Machine.UnitsLoaded {
		t.Fatalf("incomplete aggregate: %+v", snapshot)
	}
	before := *snapshotUnit(t, snapshot, "work.service")
	if before.LastOperationID != snapshot.Operations[0].ID || before.ConfigRevision != snapshot.Machine.ConfigRevision || snapshot.Operations[0].State != "running" {
		t.Fatalf("operation/unit identity mismatch: %+v", snapshot)
	}
	release()
	if err := waitErr(t, done); err != nil {
		t.Fatal(err)
	}
	after, err := m.Snapshot()
	if err == nil && (after.ManagerID != snapshot.ManagerID || after.Sequence <= snapshot.Sequence) {
		t.Fatal("snapshot capture order lost manager identity")
	}
	if err != nil || len(after.Operations) != 0 || snapshotUnit(t, after, "work.service").MainPID == 0 || snapshotUnit(t, after, "work.service").ActiveState != "active" {
		t.Fatalf("completed snapshot: %+v %v", after, err)
	}
	if snapshotUnit(t, snapshot, "work.service").MainPID != before.MainPID || snapshot.Operations[0].State != "running" {
		t.Fatal("later completion mutated a published snapshot")
	}
}

func TestSnapshotCopiesCleanupAndRetainsOwnedPID(t *testing.T) {
	l := &failedStopLauncher{}
	m := managerWith(t, l, map[string]string{"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n"})
	if _, err := m.Start(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	defer func() { l.proc.fail.Store(false); _, _ = m.Stop("work") }()
	if _, err := m.Stop("work"); err == nil {
		t.Fatal("injected cleanup failure missing")
	}
	snapshot, err := m.Snapshot()
	if err != nil || len(snapshotUnit(t, snapshot, "work.service").PendingCleanup) != 1 || snapshotUnit(t, snapshot, "work.service").MainPID == 0 {
		t.Fatalf("retained resource snapshot: %+v %v", snapshot, err)
	}
	snapshotUnit(t, snapshot, "work.service").PendingCleanup[0] = "changed"
	snapshotUnit(t, snapshot, "work.service").Name = "changed"
	current, err := m.Snapshot()
	if err != nil || snapshotUnit(t, current, "work.service").PendingCleanup[0] != "workload" || snapshotUnit(t, current, "work.service").Name != "work.service" {
		t.Fatal("caller mutated retained lifecycle state")
	}
	l.proc.fail.Store(false)
	if _, err := m.Stop("work"); err != nil {
		t.Fatal(err)
	}
	current, _ = m.Snapshot()
	if snapshotUnit(t, current, "work.service").MainPID != 0 || len(snapshotUnit(t, current, "work.service").PendingCleanup) != 0 {
		t.Fatal("confirmed cleanup did not release snapshot ownership")
	}
}

func TestSnapshotRejectsOversizedViewsWithoutPartialResults(t *testing.T) {
	m := &Manager{units: make(map[string]*unitRuntime)}
	for i := 0; i <= maxSnapshotUnits; i++ {
		m.units[fmt.Sprintf("unit-%d.target", i)] = nil
	}
	if result, err := m.Snapshot(); result != nil || err == nil {
		t.Fatal("oversized unit set returned partial success")
	}
	m.units = map[string]*unitRuntime{strings.Repeat("x", maxSnapshotBytes): nil}
	if result, err := m.Snapshot(); result != nil || err == nil {
		t.Fatal("oversized text returned an unencodable response")
	}
	m.units = nil
	m.operations = make(map[string]*protocol.OperationResult)
	m.activeOperations = make(map[string]*operationTask)
	for i := 0; i <= maxSnapshotOperations; i++ {
		id := fmt.Sprintf("op/%d", i)
		m.operations[id] = &protocol.OperationResult{ID: id, State: "running"}
		m.activeOperations[id] = &operationTask{}
	}
	if result, err := m.Snapshot(); result != nil || err == nil {
		t.Fatal("oversized operation set returned partial success")
	}
}

func TestSnapshotDoesNotQueryBlockedProcess(t *testing.T) {
	l := &delayedAliveLauncher{}
	m := managerWith(t, l, map[string]string{"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n"})
	if _, err := m.Start(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	defer close(l.process.release)
	l.process.delay.Store(true)
	done := make(chan error, 1)
	go func() { _, err := m.Snapshot(); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-l.process.entered:
		t.Fatal("snapshot queried native liveness")
	case <-time.After(time.Second):
		t.Fatal("snapshot waited on native work")
	}
}

func TestSnapshotCountsMatchConcurrentLifecycleMembers(t *testing.T) {
	m := managerWith(t, &fakeLauncher{}, map[string]string{
		"a.target": "[Unit]\nDescription=A\n",
		"b.target": "[Unit]\nDescription=B\n",
	})
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 50; i++ {
			for _, name := range []string{"a.target", "b.target"} {
				if _, err := m.Start(context.Background(), name); err != nil {
					done <- err
					return
				}
				if _, err := m.Stop(name); err != nil {
					done <- err
					return
				}
			}
		}
		done <- nil
	}()
	for {
		// Inspect the accepted snapshot and operation index at the same decision
		// point while public start/stop calls complete on the other goroutine.
		m.mu.Lock()
		indexed, indexErr := m.snapshotLocked()
		running := 0
		for id, operation := range m.operations {
			if operation.State == "running" {
				running++
				if m.activeOperations[id] == nil {
					m.mu.Unlock()
					t.Fatal("running operation vanished before terminal publication")
				}
			}
		}
		m.mu.Unlock()
		if indexErr != nil || len(indexed.Operations) != running {
			t.Fatalf("snapshot operation index omitted running work: %+v %v", indexed, indexErr)
		}
		snapshot, err := m.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		active, failed := 0, 0
		for _, member := range snapshot.Units {
			if member.ActiveState == "active" || member.ActiveState == "activating" {
				active++
			}
			if member.ActiveState == "failed" {
				failed++
			}
		}
		if snapshot.Machine.UnitsLoaded != len(snapshot.Units) || snapshot.Machine.UnitsActive != active || snapshot.Machine.UnitsFailed != failed {
			t.Fatal("aggregate mixed different lifecycle decision times")
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			return
		default:
		}
	}
}

func TestSnapshotSelectsOnlySortedActiveOperations(t *testing.T) {
	m := &Manager{operations: make(map[string]*protocol.OperationResult), activeOperations: make(map[string]*operationTask)}
	for i := 0; i < completedOperationLimit; i++ {
		id := fmt.Sprintf("completed-%d", i)
		m.operations[id] = &protocol.OperationResult{ID: id, State: "succeeded"}
	}
	for _, id := range []string{"active-c", "active-a", "active-b"} {
		m.operations[id] = &protocol.OperationResult{ID: id, State: "running"}
		m.activeOperations[id] = &operationTask{}
	}
	got, err := m.Snapshot()
	if err != nil || len(got.Operations) != 3 {
		t.Fatalf("active snapshot: %+v %v", got, err)
	}
	for i, want := range []string{"active-a", "active-b", "active-c"} {
		if got.Operations[i].ID != want {
			t.Fatalf("operation %d = %s", i, got.Operations[i].ID)
		}
	}
}

func TestOversizedSnapshotKeepsWireError(t *testing.T) {
	m := &Manager{units: map[string]*unitRuntime{strings.Repeat("x", maxSnapshotBytes): nil}}
	clientConn, serverConn := net.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer clientConn.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer serverConn.Close()
		protocol.ServeConn(ctx, serverConn, m, protocol.AllowAdmin)
	}()
	got, err := protocol.NewClient(clientConn).Snapshot(ctx)
	var protocolError *protocol.Error
	if got != nil || !errors.As(err, &protocolError) || protocolError.Code != protocol.CodeFailed || protocolError.Message != "coordinator snapshot exceeds 512 KiB; use individual status/operation queries" {
		t.Fatalf("oversized snapshot response: %+v %v", got, err)
	}
	clientConn.Close()
	<-done
}

func snapshotUnit(t *testing.T, snapshot *protocol.SnapshotResult, name string) *protocol.UnitSnapshot {
	t.Helper()
	for i := range snapshot.Units {
		if snapshot.Units[i].Name == name {
			return &snapshot.Units[i]
		}
	}
	t.Fatalf("snapshot is missing %s", name)
	return nil
}
