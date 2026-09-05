//go:build windows

package manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// These deliberately assert the desired behavior and fail on the reviewed
// baseline. Opt in explicitly; R1 removes the gate once the defects are fixed.
func requireReviewRepro(t *testing.T) {
	t.Helper()
	if os.Getenv("WINUNITD_REVIEW_REPRO") != "1" {
		t.Skip("known failing review reproduction; set WINUNITD_REVIEW_REPRO=1")
	}
}

func TestReviewReproReloadKeepsLiveUnit(t *testing.T) {
	requireReviewRepro(t)
	for _, change := range []string{"delete", "invalid"} {
		t.Run(change, func(t *testing.T) {
			dir := t.TempDir()
			m := startWindowsHelperUnit(t, dir, "review.service", "Type=simple\n", "sleep", 0, "")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := m.Start(ctx, "review.service"); err != nil {
				t.Fatal(err)
			}
			proc := waitWindowsLiveProc(t, m, "review.service")
			// Keep an independent process/job reference: the defect drops the
			// manager's reference. Cleanup must not depend on the broken lookup.
			t.Cleanup(func() {
				_ = proc.Stop(3 * time.Second)
				if proc.Alive() {
					t.Error("fixture survived independent job cleanup")
				}
				_ = proc.Close()
			})
			path := filepath.Join(dir, "units", "review.service")
			var err error
			if change == "delete" {
				err = os.Remove(path)
			} else {
				err = os.WriteFile(path, []byte("[Service]\nType=invalid\n"), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			result, err := m.Reload()
			if err != nil {
				t.Fatal(err)
			}
			if change == "invalid" && len(result.Errors) == 0 {
				t.Fatal("invalid replacement was not rejected")
			}
			if !proc.Alive() {
				t.Fatal("fixture exited before the ownership assertion")
			}
			status, err := m.Status("review.service")
			if err != nil {
				t.Errorf("live unit became unreachable through status: %v", err)
			} else if status.Unit == nil || status.Unit.ActiveState != "active" {
				t.Errorf("live unit status = %+v", status)
			}
			if _, err := m.Stop("review.service"); err != nil {
				t.Errorf("live unit became unreachable through stop: %v", err)
			} else if proc.Alive() {
				t.Error("successful stop left fixture alive")
			}
		})
	}
}

func TestReviewReproOneshotDrainsOutput(t *testing.T) {
	requireReviewRepro(t)
	for _, stream := range []string{"stdout", "stderr"} {
		for _, large := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/large=%t", stream, large), func(t *testing.T) {
				dir := t.TempDir()
				pidFile := filepath.Join(dir, "fixture.pid")
				body := fmt.Sprintf("Type=oneshot\nTimeoutStartSec=5s\nEnvironment=\"WINUNITD_REVIEW_PID_FILE=%s\"\n", pidFile)
				if large {
					body += "Environment=WINUNITD_REVIEW_LARGE=1\n"
				}
				if stream == "stderr" {
					body += "Environment=WINUNITD_REVIEW_STDERR=1\n"
				}
				m := startWindowsHelperUnit(t, dir, "review.service", body, "review-output", 0, "")
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_, err := m.Start(ctx, "review.service")
				if err != nil {
					t.Errorf("oneshot output did not drain before exit: %v", err)
				}
				pid := waitWindowsChildPID(t, pidFile)
				if windowsProcessAlive(pid) {
					// The launcher should terminate timed-out children. A bounded
					// fallback prevents a regression from leaking the fixture.
					p, findErr := os.FindProcess(pid)
					if findErr == nil {
						_ = p.Kill()
						_ = p.Release()
					}
					t.Fatal("oneshot child survived completion/timeout")
				}
			})
		}
	}
}
