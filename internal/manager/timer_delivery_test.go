package manager

import (
	"context"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/timers"
)

func TestPersistentCalendarCatchupCoalescesWithActiveService(t *testing.T) {
	launch := &fakeLauncher{}
	m, clock := managerWithFake(t, launch, map[string]string{
		"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n",
		"work.timer":   "[Timer]\nOnCalendar=daily\nPersistent=yes\n",
	})
	store, err := timers.OpenStore(m.cfg.TimerStateDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save("work.timer", timers.Runtime{LastActual: clock.Now().Add(-72 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(context.Background(), "work.service"); err != nil {
		t.Fatal(err)
	}
	before, err := m.Status("work.service")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(context.Background(), "work.timer"); err != nil {
		t.Fatal(err)
	}
	previous := ""
	for cycle := 0; cycle < 2; cycle++ {
		if cycle != 0 {
			clock.Suspend(72 * time.Hour)
			m.engine.ClockChanged()
		}
		waitCond(t, func() bool {
			st, err := m.Status("work.timer")
			return err == nil && st.Unit.TimerActivation != nil && st.Unit.TimerActivation.ID != previous && st.Unit.TimerActivation.Result == "success"
		})
		st, err := m.Status("work.timer")
		if err != nil {
			t.Fatal(err)
		}
		previous = st.Unit.TimerActivation.ID
		if st.Unit.Last != clock.Now().UTC().Format(time.RFC3339Nano) {
			t.Fatalf("catch-up did not coalesce through current time: %+v", st.Unit.TimerActivation)
		}
		after, err := m.Status("work.service")
		if err != nil || after.Unit.InvocationID != before.Unit.InvocationID || len(launch.units()) != 1 {
			t.Fatalf("catch-up replaced or duplicated the active invocation: %+v, %v", after, err)
		}
	}
	if _, err := m.Stop("work.timer"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stop("work.service"); err != nil {
		t.Fatal(err)
	}
}
