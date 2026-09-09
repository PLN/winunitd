package manager

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/pathwatch"
	"github.com/PLN/winunitd/internal/protocol"
)

func TestWatchStopPublishesUncertaintyAndRejectsLateFailure(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	w := &controlledCloseWatch{release: release}
	m := managerWithPath(t, &fakeLauncher{}, func(pathwatch.Spec) (pathwatch.Watch, error) { return w, nil }, map[string]string{
		"work.path":    "[Path]\nPathChanged=C:\\Data\\incoming\n",
		"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n",
	})
	if _, err := m.Start(context.Background(), "work.path"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	h := m.units["work.path"].hub
	m.mu.Unlock()
	done := make(chan error, 1)
	go func() { _, err := m.Stop("work.path"); done <- err }()
	waitCond(t, func() bool { return w.calls.Load() == 1 })
	m.failHub("work.path", h, errors.New("late native watch failure"))
	got, err := m.Handle(context.Background(), protocol.MethodStatus, json.RawMessage(`{"unit":"work.path"}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Unit struct {
			ActiveState, SubState, Error string
			TerminationUncertain         bool
		}
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if !wire.Unit.TerminationUncertain || wire.Unit.SubState != "stop" || wire.Unit.ActiveState != "deactivating" || wire.Unit.Error != "" {
		t.Fatalf("pending stop diagnostics: %s", raw)
	}
	listed, err := m.ListUnits()
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range listed.Units {
		if u.Name == "work.path" && !u.TerminationUncertain {
			t.Fatal("list omitted uncertainty")
		}
	}
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("stop did not finish")
	}
	st, err := m.Status("work.path")
	if err != nil {
		t.Fatal(err)
	}
	if st.Unit.TerminationUncertain || st.Unit.SubState != "" || st.Unit.ActiveState != "inactive" {
		t.Fatalf("completed stop: %+v", st.Unit)
	}
}

func TestRecoveryAdmissionRetainsUnresolvedProcess(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		m := managerWith(t, &fakeLauncher{}, map[string]string{"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n"})
		if _, err := m.Start(context.Background(), "work"); err != nil {
			t.Fatal(err)
		}
		m.mu.Lock()
		rt := m.units["work.service"]
		rt.stopUncertain, rt.err = uncertain, "retained cleanup diagnostic"
		owner := runtimeIdentity{name: "work.service", record: rt, gen: rt.gen}
		m.mu.Unlock()
		ctx := m.acceptRecovery(recoveryRequest{owner: owner})
		if ctx != nil {
			t.Fatal("recovery accepted an owned process")
		}
		m.mu.Lock()
		if rt.state != core.Active || rt.err != "retained cleanup diagnostic" || rt.restartCancel != nil {
			t.Error("rejected recovery changed lifecycle")
		}
		m.mu.Unlock()
	}
}

func TestRecoveryAdmissionRetainsUncertainCleanupWithoutProcess(t *testing.T) {
	rt := &unitRuntime{state: core.Failed, stopUncertain: true, err: "watch cleanup pending"}
	m := &Manager{units: map[string]*unitRuntime{"work.service": rt}}
	if m.acceptRecovery(recoveryRequest{owner: runtimeIdentity{name: "work.service", record: rt}}) != nil {
		t.Fatal("recovery accepted unresolved cleanup")
	}
	if rt.state != core.Failed || rt.err != "watch cleanup pending" {
		t.Fatal("cleanup diagnostic overwritten")
	}
}

func TestStartPublicationStatePairs(t *testing.T) {
	for _, tc := range []struct {
		from    core.State
		sub     core.Substate
		outcome core.State
		want    core.Substate
	}{
		{core.Inactive, core.SubNone, core.Active, core.SubRunning},
		{core.Failed, core.SubWatchdog, core.Active, core.SubRunning},
		{core.Activating, core.SubStart, core.Failed, core.SubNone},
		{core.Failed, core.SubWatchdog, core.Failed, core.SubWatchdog},
		{core.Failed, core.SubRunning, core.Failed, core.SubNone},
		{core.Activating, core.SubStart, core.Inactive, core.SubNone},
	} {
		rt := &unitRuntime{state: tc.from, sub: tc.sub}
		rt.publishStartOutcome(tc.outcome)
		if rt.state != tc.outcome || rt.sub != tc.want {
			t.Fatalf("%v/%v -> %v: got %v/%v", tc.from, tc.sub, tc.outcome, rt.state, rt.sub)
		}
	}
}
