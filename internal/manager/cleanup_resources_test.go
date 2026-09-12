package manager

import (
	"errors"
	"testing"

	"github.com/PLN/winunitd/internal/core"
)

func TestWatchCleanupCannotClearNotificationFailure(t *testing.T) {
	nrt, hub := &notifyRuntime{}, &watchRuntime{}
	rt := &unitRuntime{state: core.Failed, notify: nrt, hub: hub}
	m := &Manager{units: map[string]*unitRuntime{"work.service": rt}}
	m.applyNotifyCleanup(notifyCleanup{name: "work.service", notify: nrt, err: errors.New("notification close failed")})
	m.applyHubCleanup(hubCleanup{name: "work.service", hub: hub})
	if !rt.cleanupPending() || rt.notify != nrt || rt.hub != nil {
		t.Fatal("watch success erased unresolved notification ownership")
	}
}

func TestWorkloadCleanupCannotClearNotificationFailure(t *testing.T) {
	proc, nrt := &fakeProc{done: make(chan struct{})}, &notifyRuntime{}
	rt := &unitRuntime{state: core.Failed, proc: proc, notify: nrt, cleanup: cleanupWorkload}
	m := &Manager{units: map[string]*unitRuntime{"work.service": rt}}
	owner := runtimeIdentity{name: "work.service", record: rt}
	m.applyNotifyCleanup(notifyCleanup{name: owner.name, notify: nrt, err: errors.New("notification close failed")})
	m.applyStopCleanup(workloadCleanup{owner: owner, process: proc})
	if rt.proc != nil || rt.cleanup != cleanupNotify || rt.notify != nrt {
		t.Fatal("workload result erased another resource or retained a confirmed process")
	}
	// A result for a replaced handle cannot clear current cleanup authority.
	m.applyNotifyCleanup(notifyCleanup{name: owner.name, notify: &notifyRuntime{}})
	if rt.cleanup != cleanupNotify {
		t.Fatal("stale handle cleared notification cleanup")
	}
	m.applyNotifyCleanup(notifyCleanup{name: owner.name, notify: nrt})
	if rt.cleanupPending() || rt.notify != nil {
		t.Fatal("matching retry did not complete cleanup")
	}
}
