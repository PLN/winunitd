package manager

import (
	"context"
	"fmt"
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
	result, err := m.Handle(context.Background(), protocol.MethodSnapshot, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := result.(*protocol.SnapshotResult)
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
	for i := 0; i <= maxSnapshotOperations; i++ {
		id := fmt.Sprintf("op/%d", i)
		m.operations[id] = &protocol.OperationResult{ID: id, State: "running"}
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
