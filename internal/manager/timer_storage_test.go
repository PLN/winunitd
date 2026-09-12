package manager

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/timers"
)

func TestTimerStorageFailureIsVisibleAndRepairable(t *testing.T) {
	launch := &fakeLauncher{}
	m, clock := managerWithFake(t, launch, map[string]string{
		"work.timer":   "[Timer]\nOnStartupSec=1s\n",
		"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n",
	})
	statePath := filepath.Join(m.cfg.TimerStateDir(), "work.timer.json")
	if err := os.WriteFile(statePath, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(context.Background(), "work.timer"); err != nil {
		t.Fatal(err)
	}
	waitCond(t, func() bool { st, _ := m.Status("work.timer"); return st.Unit.TimerStorageState == "failed" })
	listed, err := m.ListTimers()
	if err != nil || len(listed.Timers) != 1 || listed.Timers[0].StorageError == "" || listed.Timers[0].Next != "" {
		t.Fatalf("failure omitted from timer list: %+v %v", listed, err)
	}
	clock.Advance(2 * time.Second)
	m.engine.ClockChanged()
	if len(launch.specs()) != 0 {
		t.Fatal("corrupt state allowed activation")
	}
	if _, err := m.Stop("work.timer"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(context.Background(), "work.timer"); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, "work.service", core.Active)
	st, err := m.Status("work.timer")
	if err != nil || st.Unit.TimerStorageState != "ready" || st.Unit.TimerStorageError != "" {
		t.Fatalf("repair not visible: %+v %v", st, err)
	}
}

func TestPersistentTimerRecoveryCoalescesActiveCompanion(t *testing.T) {
	launch := &fakeLauncher{}
	m, clock := managerWithFake(t, launch, map[string]string{
		"work.timer":   "[Timer]\nOnCalendar=daily\nPersistent=true\n",
		"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n",
	})
	if _, err := m.Start(context.Background(), "work.service"); err != nil {
		t.Fatal(err)
	}
	store, err := timers.OpenStore(m.cfg.TimerStateDir())
	if err != nil {
		t.Fatal(err)
	}
	old := clock.Now().Add(-48 * time.Hour)
	intent := timers.Activation{ID: "interrupted-fixture", Unit: "work.service", Result: "pending", Scheduled: old, Actual: old}
	if err := store.Save("work.timer", timers.Runtime{Activation: intent, LastActual: old, LastScheduled: old}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(context.Background(), "work.timer"); err != nil {
		t.Fatal(err)
	}
	waitCond(t, func() bool {
		st, _ := m.Status("work.timer")
		return st.Unit.TimerActivation != nil && st.Unit.TimerActivation.Result == "success"
	})
	if len(launch.specs()) != 1 {
		t.Fatal("recovered intent duplicated active companion")
	}
	listed, err := m.ListTimers()
	if err != nil || len(listed.Timers) != 1 || listed.Timers[0].Activation == nil || listed.Timers[0].Activation.ID != intent.ID {
		t.Fatalf("intent missing from timer list: %+v %v", listed, err)
	}
}
