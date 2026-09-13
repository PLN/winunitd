//go:build windows

package manager

import (
	"context"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
)

func TestWindowsRestartBackoffRespectsNativeFailureLimit(t *testing.T) {
	body := "[Unit]\nFormatVersion=2\nStartLimitIntervalSec=60s\nStartLimitBurst=4\n[Service]\nType=simple\nRestart=on-failure\nRestartSec=100ms\nRestartBackoff=exponential\nRestartMaxDelaySec=250ms\n"
	m := startWindowsHelperUnit(t, t.TempDir(), "failed.service", body, "exit", 7, "")
	started := time.Now()
	_, _ = m.Start(context.Background(), "failed.service")
	waitUntil(t, 10*time.Second, func() bool {
		status, err := m.Status("failed.service")
		return err == nil && status.Unit.Reason == core.ReasonStartLimit && status.Unit.MainPID == 0 && len(status.Unit.PendingCleanup) == 0
	})
	if elapsed := time.Since(started); elapsed < 550*time.Millisecond {
		t.Fatalf("native retries bypassed backoff: %v", elapsed)
	}
	status, err := m.Status("failed.service")
	if err != nil || status.Unit.RestartAttempt != 3 || status.Unit.RestartDelaySec != 0 {
		t.Fatalf("terminal backoff status: %+v %v", status, err)
	}
	snapshot, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	u := snapshotUnit(t, snapshot, "failed.service")
	if u.RestartAttempt != 3 || u.RestartDelaySec != 0 || u.Reason != status.Unit.Reason || u.InvocationID != status.Unit.InvocationID {
		t.Fatal("native snapshot lost accepted backoff identity")
	}
	if _, err := m.Stop("failed.service"); err != nil {
		t.Fatal(err)
	}
}
