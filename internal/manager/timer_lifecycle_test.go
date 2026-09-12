package manager

import (
	"context"
	"testing"

	"github.com/PLN/winunitd/internal/timers"
)

func TestTimerArmSnapshotRetainsRevisionUntilRearm(t *testing.T) {
	launch := &fakeLauncher{}
	m, _ := managerWithFake(t, launch, map[string]string{
		"work.timer":   "[Timer]\nOnStartupSec=1h\n",
		"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n",
	})
	if _, err := m.Start(context.Background(), "work.timer"); err != nil {
		t.Fatal(err)
	}
	first, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	initial := snapshotUnit(t, first, "work.timer")
	if initial.ArmedConfigRevision == "" || initial.ArmedConfigRevision != initial.ConfigRevision {
		t.Fatal("accepted arm revision missing")
	}
	m.mu.Lock()
	oldToken := m.units["work.timer"].timer.token
	m.mu.Unlock()
	writeUnit(t, m.cfg.UnitsDir(), "work.timer", "[Timer]\nOnStartupSec=2h\n")
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	reloaded, _ := m.Snapshot()
	current := snapshotUnit(t, reloaded, "work.timer")
	if current.ConfigRevision == initial.ConfigRevision || current.ArmedConfigRevision != initial.ArmedConfigRevision {
		t.Fatal("reload rewrote the captured arm")
	}
	if _, err := m.Stop("work.timer"); err != nil {
		t.Fatal(err)
	}
	stopped, _ := m.Snapshot()
	if snapshotUnit(t, stopped, "work.timer").ArmedConfigRevision != "" {
		t.Fatal("stop retained timer admission")
	}
	if _, err := m.Start(context.Background(), "work.timer"); err != nil {
		t.Fatal(err)
	}
	rearmed, _ := m.Snapshot()
	current = snapshotUnit(t, rearmed, "work.timer")
	if current.ArmedConfigRevision != current.ConfigRevision {
		t.Fatal("fresh arm did not adopt accepted revision")
	}
	m.onTimerElapsed(timers.Fire{Name: "work.timer", Unit: "work.service", Token: oldToken})
	if len(launch.specs()) != 0 {
		t.Fatal("stale arm launched a companion")
	}
	if err := m.CloseContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	closed, _ := m.Snapshot()
	if closed.Machine.State != "closing" || snapshotUnit(t, closed, "work.timer").ArmedConfigRevision != "" {
		t.Fatal("close did not revoke accepted timer arm")
	}
}
