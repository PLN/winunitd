//go:build windows

package manager

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
)

func TestWindowsSnapshotTerminalFailureReasons(t *testing.T) {
	for _, tc := range []struct {
		name, restart, reason string
		exit                  int
	}{
		{"start-limit", "on-failure", core.ReasonStartLimit, 7},
		{"abnormal-exit", "no", core.ReasonSignalEquivalent, int(uint32(0xC0000005))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf("[Unit]\nStartLimitIntervalSec=60s\nStartLimitBurst=3\n[Service]\nType=simple\nRestart=%s\nRestartSec=20ms\n", tc.restart)
			m := startWindowsHelperUnit(t, t.TempDir(), "failed.service", body, "exit", tc.exit, "")
			// An immediate exit may precede completion of the initial Start call.
			_, _ = m.Start(context.Background(), "failed.service")
			waitUntil(t, 10*time.Second, func() bool {
				status, err := m.Status("failed.service")
				return err == nil && status.Unit.ActiveState == "failed" && status.Unit.Reason == tc.reason && status.Unit.MainPID == 0 && len(status.Unit.PendingCleanup) == 0
			})
			status, err := m.Status("failed.service")
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := m.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			u := snapshotUnit(t, snapshot, "failed.service")
			if u.Error == "" || u.Error != status.Unit.Error || u.Reason != status.Unit.Reason || u.InvocationID != status.Unit.InvocationID || u.ConfigRevision != status.Unit.ConfigRevision || u.InvocationConfigRevision != status.Unit.InvocationConfigRevision || u.MainPID != 0 || len(u.PendingCleanup) != 0 {
				t.Fatalf("native terminal diagnostics mismatch: snapshot=%+v status=%+v", u, status.Unit)
			}
		})
	}
}
