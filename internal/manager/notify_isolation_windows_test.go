//go:build windows

package manager

import (
	"context"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/notify"
)

func TestWindowsNotifyManagersUseDistinctInvocationEndpoints(t *testing.T) {
	const name = "notify-isolation.service"
	const body = "Type=notify\nTimeoutStartSec=5s\nTimeoutStopSec=2s\nWatchdogSec=2s\nEnvironment=WINUNITD_WATCHDOG_EVERY=100\n"
	first := startWindowsHelperUnit(t, t.TempDir(), name, body, "notify-watchdog", 0, "")
	second := startWindowsHelperUnit(t, t.TempDir(), name, body, "notify-watchdog", 0, "")
	for i, m := range []*Manager{first, second} {
		if _, err := m.Start(context.Background(), name); err != nil {
			t.Fatalf("manager %d could not start the same unit name: %v", i+1, err)
		}
	}
	address := func(m *Manager) string {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.units[name].notify.Addr()
	}
	old, other := address(first), address(second)
	if old == "" || old == other {
		t.Fatal("independent managers share a notification endpoint")
	}
	// Both real process trees must keep receiving their own heartbeat traffic.
	time.Sleep(2200 * time.Millisecond)
	for _, m := range []*Manager{first, second} {
		assertState(t, m, name, core.Active)
	}
	if _, err := first.Stop(name); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Start(context.Background(), name); err != nil {
		t.Fatal(err)
	}
	if current := address(first); current == old || current == other {
		t.Fatal("replacement invocation reused an earlier notification endpoint")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := notify.Send(ctx, old, notify.Message{Ready: true, Watchdog: true}); err == nil {
		t.Fatal("stale endpoint accepted a notification after replacement")
	}
	assertState(t, first, name, core.Active)
	assertState(t, second, name, core.Active)
	for _, m := range []*Manager{first, second} {
		if _, err := m.Stop(name); err != nil {
			t.Fatal(err)
		}
		status, err := m.Status(name)
		if err != nil || status.Unit.MainPID != 0 || status.Unit.TerminationUncertain {
			t.Fatalf("notification fixture retained process ownership: %+v %v", status, err)
		}
	}
}
