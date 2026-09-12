package manager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
)

const boundWorker = "[Service]\nExecStart=C:\\Tools\\worker.exe\n"

func TestBoundExitStopsDependentWithoutRestartingIt(t *testing.T) {
	m, clock := managerWithFake(t, &fakeLauncher{}, map[string]string{
		"peer.service":     boundWorker + "Restart=always\nRestartSec=1s\n",
		"bound.service":    "[Unit]\nBindsTo=peer.service\nAfter=peer.service\n" + boundWorker + "Restart=always\n",
		"required.service": "[Unit]\nRequires=peer.service\nAfter=peer.service\n" + boundWorker,
		"part.service":     "[Unit]\nPartOf=peer.service\n" + boundWorker,
	})
	for _, name := range []string{"bound.service", "required.service", "part.service"} {
		if _, err := m.Start(context.Background(), name); err != nil {
			t.Fatal(err)
		}
	}
	m.mu.Lock()
	peer := m.units["peer.service"].proc.(*fakeProc)
	m.mu.Unlock()
	peer.die(1)
	waitCond(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.units["bound.service"].state == core.Inactive && m.boundStopsDone == nil && m.units["peer.service"].sub == core.SubAutoRestart && clock.WaitingAt(time.Second)
	})
	clock.Advance(time.Second)
	waitCond(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.units["peer.service"].state == core.Active && m.units["peer.service"].proc != peer
	})
	for _, name := range []string{"required.service", "part.service"} {
		assertState(t, m, name, core.Active)
	}
	assertState(t, m, "bound.service", core.Inactive)
	st, err := m.Status("bound.service")
	if err != nil {
		t.Fatal(err)
	}
	op, err := m.Operation(st.Unit.LastOperationID)
	if err != nil || op.Origin != "dependency" || op.State != "succeeded" {
		t.Fatalf("dependency operation: %+v, %v", op, err)
	}
}

func TestBoundAfterRequiresActiveOneshotPeer(t *testing.T) {
	for _, retained := range []bool{false, true} {
		directive := "RemainAfterExit=no\n"
		if retained {
			directive = "RemainAfterExit=yes\n"
		}
		m := testManager(t, map[string]string{
			"peer.service":  oneshotBody + directive,
			"bound.service": "[Unit]\nBindsTo=peer.service\nAfter=peer.service\n" + boundWorker,
		})
		result, err := m.Start(context.Background(), "bound.service")
		want := core.Inactive
		if retained {
			want = core.Active
		}
		if err != nil || result.ActiveState != want.String() {
			t.Fatalf("retained=%v: %+v, %v", retained, result, err)
		}
	}
}

func TestBoundExitRetainsInvocationPolicyAfterReload(t *testing.T) {
	m := testManager(t, map[string]string{
		"peer.service":  boundWorker,
		"bound.service": "[Unit]\nBindsTo=peer.service\nAfter=peer.service\n" + boundWorker,
	})
	if _, err := m.Start(context.Background(), "bound.service"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(m.cfg.UnitsDir(), "bound.service")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	peer := m.units["peer.service"].proc.(*fakeProc)
	m.mu.Unlock()
	peer.die(0)
	waitCond(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.units["bound.service"].state == core.Inactive && m.boundStopsDone == nil
	})
}

func TestBoundStopRejectsOldInvocation(t *testing.T) {
	m := testManager(t, map[string]string{"work.service": boundWorker})
	if _, err := m.Start(context.Background(), "work.service"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	rt := m.units["work.service"]
	old := boundStopMember{owner: runtimeIdentity{name: "work.service", record: rt, gen: rt.gen}, epoch: rt.stopEpoch, unit: rt.ownedUnit()}
	m.mu.Unlock()
	if _, err := m.Restart(context.Background(), "work.service"); err != nil {
		t.Fatal(err)
	}
	if err := m.stopBoundMember(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "work.service", core.Active)
}

func TestDelayedBoundBatchPreservesReplacementMetadata(t *testing.T) {
	m := testManager(t, map[string]string{"work.service": boundWorker})
	if _, err := m.Start(context.Background(), "work.service"); err != nil {
		t.Fatal(err)
	}
	// Hold an accepted batch before its worker is scheduled, then admit a
	// replacement through the public restart path before delivering the batch.
	m.mu.Lock()
	rt := m.units["work.service"]
	m.disarmStartLocked("work.service")
	rt.operations++
	m.boundStops = map[string]boundStopMember{"work.service": {
		owner: runtimeIdentity{name: "work.service", record: rt, gen: rt.gen}, epoch: rt.stopEpoch, unit: rt.ownedUnit(),
	}}
	m.boundStopsDone = make(chan struct{})
	done := m.boundStopsDone
	m.mu.Unlock()
	// Even a failing assertion must release the retained fixture batch.
	var once sync.Once
	deliver := func() { once.Do(func() { go m.runBoundStops() }) }
	t.Cleanup(deliver)
	result, err := m.Restart(context.Background(), "work.service")
	if err != nil {
		t.Fatal(err)
	}
	deliver()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("delayed batch did not complete")
	}
	st, err := m.Status("work.service")
	if err != nil || st.Unit.ActiveState != "active" || st.Unit.LastOperationID != result.OperationID {
		t.Fatalf("old batch changed replacement: %+v, %v", st, err)
	}
}

func TestBoundStopProgressWithClientStopCapacityFull(t *testing.T) {
	launch := &phasedOperationLauncher{entered: make(chan string, 8), release: map[string]chan struct{}{
		"busy.service": make(chan struct{}), "peer.service": make(chan struct{}), "bound.service": make(chan struct{}),
	}}
	close(launch.release["peer.service"])
	close(launch.release["bound.service"])
	m := managerWith(t, launch, map[string]string{
		"busy.service":  boundWorker,
		"peer.service":  boundWorker,
		"bound.service": "[Unit]\nBindsTo=peer.service\nAfter=peer.service\n" + boundWorker,
	})
	m.cfg.MaxStopTransactions = 1
	var once sync.Once
	unblock := func() { once.Do(func() { close(launch.release["busy.service"]) }) }
	t.Cleanup(unblock)
	for _, name := range []string{"busy.service", "bound.service"} {
		if _, err := m.Start(context.Background(), name); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error, 1)
	go func() { _, err := m.Stop("busy.service"); done <- err }()
	select {
	case <-launch.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("client stop did not occupy capacity")
	}
	if _, err := m.Stop("bound.service"); err == nil {
		t.Fatal("client stop capacity was not full")
	}
	m.mu.Lock()
	peer := m.units["peer.service"].proc.(*phasedOperationProcess).Process.(*fakeProc)
	m.mu.Unlock()
	peer.die(1)
	waitCond(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.units["bound.service"].state == core.Inactive && m.boundStopsDone == nil
	})
	unblock()
	if err := waitErr(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestBoundStopOrdersAndDisarmsCompleteScope(t *testing.T) {
	launch := &phasedOperationLauncher{entered: make(chan string, 8), release: map[string]chan struct{}{
		"peer.service": make(chan struct{}), "a.service": make(chan struct{}), "b.service": make(chan struct{}),
	}}
	close(launch.release["peer.service"])
	close(launch.release["b.service"])
	m := managerWith(t, launch, map[string]string{
		"peer.service": boundWorker,
		"a.service":    "[Unit]\nBindsTo=peer.service\nAfter=peer.service b.service\n" + boundWorker,
		"b.service":    "[Unit]\nBindsTo=peer.service\nAfter=peer.service\n" + boundWorker + "Restart=always\n",
	})
	var once sync.Once
	unblock := func() { once.Do(func() { close(launch.release["a.service"]) }) }
	t.Cleanup(unblock)
	for _, name := range []string{"b.service", "a.service"} {
		if _, err := m.Start(context.Background(), name); err != nil {
			t.Fatal(err)
		}
	}
	m.mu.Lock()
	peer := m.units["peer.service"].proc.(*phasedOperationProcess).Process.(*fakeProc)
	b := m.units["b.service"]
	owner := runtimeIdentity{name: "b.service", record: b, gen: b.gen}
	m.mu.Unlock()
	peer.die(1)
	for {
		select {
		case name := <-launch.entered:
			if name == "peer.service" {
				continue
			}
			if name != "a.service" {
				t.Fatalf("dependent stop order: %s", name)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("ordered dependency stop did not begin")
		}
		break
	}
	m.beginRestart(recoveryRequest{owner: owner})
	m.mu.Lock()
	if !b.stopping || b.restartCancel != nil || b.sub == core.SubAutoRestart {
		m.mu.Unlock()
		t.Fatal("ordered member admitted recovery before teardown")
	}
	m.mu.Unlock()
	unblock()
	waitCond(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.boundStopsDone == nil && b.state == core.Inactive
	})
}

func TestBoundStopRejectsStalePeerAndWatchdogEvents(t *testing.T) {
	m := testManager(t, map[string]string{
		"peer.service":  boundWorker,
		"bound.service": "[Unit]\nBindsTo=peer.service\nAfter=peer.service\n" + boundWorker,
	})
	if _, err := m.Start(context.Background(), "bound.service"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	rt := m.units["peer.service"]
	old := rt.proc
	owner := runtimeIdentity{name: "peer.service", record: rt, gen: rt.gen}
	m.mu.Unlock()
	if _, err := m.Restart(context.Background(), "peer.service"); err != nil {
		t.Fatal(err)
	}
	if effect := m.acceptProcessExitCleanup("peer.service", old); effect != nil {
		t.Fatal("old process exit accepted")
	}
	if effect := m.acceptWatchdogFailure(owner); effect != nil {
		t.Fatal("old watchdog accepted")
	}
	assertState(t, m, "bound.service", core.Active)
	m.mu.Lock()
	current := runtimeIdentity{name: "peer.service", record: rt, gen: rt.gen}
	m.mu.Unlock()
	m.onWatchdogTimeout(current)
	waitCond(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.units["bound.service"].state == core.Inactive && m.boundStopsDone == nil
	})
}

func TestCloseRetainsBoundStopWorker(t *testing.T) {
	launch := &phasedOperationLauncher{entered: make(chan string, 8), release: map[string]chan struct{}{
		"peer.service": make(chan struct{}), "bound.service": make(chan struct{}),
	}}
	close(launch.release["peer.service"])
	m := managerWith(t, launch, map[string]string{
		"peer.service":  boundWorker,
		"bound.service": "[Unit]\nBindsTo=peer.service\nAfter=peer.service\n" + boundWorker,
	})
	var once sync.Once
	unblock := func() { once.Do(func() { close(launch.release["bound.service"]) }) }
	t.Cleanup(unblock)
	if _, err := m.Start(context.Background(), "bound.service"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	peer := m.units["peer.service"].proc.(*phasedOperationProcess).Process.(*fakeProc)
	m.mu.Unlock()
	peer.die(1)
	for {
		select {
		case name := <-launch.entered:
			if name != "bound.service" {
				continue
			}
		case <-time.After(5 * time.Second):
			t.Fatal("bound stop did not begin")
		}
		break
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := m.CloseContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close abandoned dependency cleanup: %v", err)
	}
	m.mu.Lock()
	retained := m.boundStopsDone != nil && m.units["bound.service"].operations != 0
	m.mu.Unlock()
	if !retained {
		t.Fatal("timed-out close lost bound stop ownership")
	}
	unblock()
	retry, cancelRetry := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelRetry()
	if err := m.CloseContext(retry); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.boundStopsDone != nil || len(m.boundStops) != 0 || m.units["bound.service"].operations != 0 {
		t.Fatal("completed bound worker retained admission or records")
	}
}

func TestCloseDrainsBoundStopAcceptedBeforeWorkerStarts(t *testing.T) {
	m := testManager(t, map[string]string{
		"peer.service":  boundWorker,
		"bound.service": "[Unit]\nBindsTo=peer.service\nAfter=peer.service\n" + boundWorker,
	})
	if _, err := m.Start(context.Background(), "bound.service"); err != nil {
		t.Fatal(err)
	}
	// Serialize the accepted disappearance and close admission before its
	// worker can run. The accepted stop retains its independent lifetime.
	m.mu.Lock()
	m.queueBoundStopsLocked("peer.service")
	m.closed = true
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.CloseContext(ctx); err != nil {
		t.Fatal(err)
	}
	assertState(t, m, "bound.service", core.Inactive)
}

func TestBoundStartDuringPeerExitCleanup(t *testing.T) {
	for _, ordered := range []bool{false, true} {
		launch := &phasedOperationLauncher{entered: make(chan string, 8), release: map[string]chan struct{}{
			"peer.service": make(chan struct{}), "bound.service": make(chan struct{}),
		}}
		close(launch.release["bound.service"])
		after := ""
		if ordered {
			after = "After=peer.service\n"
		}
		m := managerWith(t, launch, map[string]string{
			"peer.service":  boundWorker + "TimeoutStopSec=30s\n",
			"bound.service": "[Unit]\nBindsTo=peer.service\n" + after + boundWorker,
		})
		var peerOnce, gateOnce sync.Once
		releasePeer := func() { peerOnce.Do(func() { close(launch.release["peer.service"]) }) }
		unlock := m.ops.lock("bound.service")
		releaseGate := func() { gateOnce.Do(unlock) }
		t.Cleanup(releasePeer)
		t.Cleanup(releaseGate)
		started := make(chan error, 1)
		go func() { _, err := m.Start(context.Background(), "bound.service"); started <- err }()
		waitCond(t, func() bool {
			m.mu.Lock()
			defer m.mu.Unlock()
			return m.units["peer.service"].state == core.Active
		})
		m.mu.Lock()
		peer := m.units["peer.service"].proc.(*phasedOperationProcess).Process.(*fakeProc)
		m.mu.Unlock()
		peer.die(1)
		select {
		case name := <-launch.entered:
			if name != "peer.service" {
				t.Fatalf("unexpected cleanup: %s", name)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("peer cleanup did not enter")
		}
		releaseGate()
		if err := waitErr(t, started); err != nil {
			t.Fatal(err)
		}
		if ordered {
			assertState(t, m, "bound.service", core.Inactive)
		} else {
			assertState(t, m, "bound.service", core.Active)
		}
		releasePeer()
		waitCond(t, func() bool {
			m.mu.Lock()
			defer m.mu.Unlock()
			return m.units["bound.service"].state == core.Inactive && m.boundStopsDone == nil
		})
	}
}

func TestBoundRetainedPeerEnteringRecoveryStopsDependent(t *testing.T) {
	m := testManager(t, map[string]string{
		"peer.service":  oneshotBody + "RemainAfterExit=yes\n",
		"bound.service": "[Unit]\nBindsTo=peer.service\nAfter=peer.service\n" + boundWorker,
	})
	if _, err := m.Start(context.Background(), "bound.service"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	rt := m.units["peer.service"]
	owner := runtimeIdentity{name: "peer.service", record: rt, gen: rt.gen}
	svc := rt.ownedUnit().Service
	m.mu.Unlock()
	// Successful retained completion alone is not disappearance.
	m.applyProcessExit(processExitCompletion{owner: owner, service: svc})
	assertState(t, m, "bound.service", core.Active)
	// Deliver an accepted recovery transition separately from process exit.
	// A retained oneshot has no remaining process watcher to deliver it again.
	if ctx := m.acceptRecovery(recoveryRequest{owner: owner}); ctx == nil {
		t.Fatal("retained peer did not enter recovery")
	}
	waitCond(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.units["bound.service"].state == core.Inactive && m.boundStopsDone == nil
	})
}

func TestBoundPeerStartLimitStopsDependent(t *testing.T) {
	m := testManager(t, map[string]string{
		"peer.service":  oneshotBody + "RemainAfterExit=yes\n",
		"bound.service": "[Unit]\nBindsTo=peer.service\nAfter=peer.service\n" + boundWorker,
	})
	if _, err := m.Start(context.Background(), "bound.service"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.failStartLimitLocked(m.units["peer.service"])
	m.mu.Unlock()
	waitCond(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.units["bound.service"].state == core.Inactive && m.boundStopsDone == nil
	})
}
