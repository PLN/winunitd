//go:build windows

package manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/journal"
	"github.com/PLN/winunitd/internal/protocol"
)

// The original review reproductions now run as required regressions.

func TestWindowsOneshotNoNewlineFragments(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			dir := t.TempDir()
			body := fmt.Sprintf("Type=oneshot\nTimeoutStartSec=5s\nEnvironment=\"WINUNITD_REVIEW_PID_FILE=%s\"\nEnvironment=WINUNITD_REVIEW_NO_NEWLINE=1\n", filepath.Join(dir, "fixture.pid"))
			if stream == "stderr" {
				body += "Environment=WINUNITD_REVIEW_STDERR=1\n"
			}
			m := startWindowsHelperUnit(t, dir, "review.service", body, "review-output", 0, "")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := m.Start(ctx, "review.service"); err != nil {
				t.Fatal(err)
			}
			logs, err := m.Logs(protocol.LogsParams{Unit: "review.service"})
			if err != nil {
				t.Fatal(err)
			}
			if len(logs.Entries) != 3 || logs.More {
				t.Fatalf("entries=%d more=%v", len(logs.Entries), logs.More)
			}
			var text strings.Builder
			for i, e := range logs.Entries {
				if e.Stream != stream || !e.Partial || e.Continuation != (i > 0) || len(e.Message) > journal.MaxCaptureFragment {
					t.Fatalf("invalid fragment %d", i)
				}
				text.WriteString(e.Message)
			}
			if text.String() != strings.Repeat("x", journal.MaxCaptureFragment*2+1) {
				t.Fatal("oneshot output lost bytes")
			}
		})
	}
}

func TestReviewReproReloadKeepsLiveUnit(t *testing.T) {
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
			original, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
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
			wantLoad := "unavailable"
			if change == "invalid" {
				wantLoad = "loaded" // atomic rejection retains the accepted definition
			}
			if status != nil && status.Unit != nil && status.Unit.LoadState != wantLoad {
				t.Errorf("load state after %s: %+v", change, status.Unit)
			}
			if _, err := m.Logs(protocol.LogsParams{Unit: "review.service"}); err != nil {
				t.Errorf("live unit became unreachable through logs: %v", err)
			}
			// Restoring the same name must reuse the live invocation.
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Reload(); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Start(ctx, "review.service"); err != nil {
				t.Fatal(err)
			}
			m.mu.Lock()
			same := m.procOfLocked("review.service") == proc
			m.mu.Unlock()
			if !same {
				t.Fatal("restored unit replaced its live process")
			}
			// Make it unavailable again before proving stop still works.
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Reload(); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Stop("review.service"); err != nil {
				t.Errorf("live unit became unreachable through stop: %v", err)
			} else if proc.Alive() {
				t.Error("successful stop left fixture alive")
			}
			if _, err := m.Start(ctx, "review.service"); err == nil {
				t.Error("stopped unit without valid configuration started again")
			}
		})
	}
}

func TestReviewReproOneshotDrainsOutput(t *testing.T) {
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
				count := 0
				cursor := ""
				for {
					logs, logErr := m.Logs(protocol.LogsParams{Unit: "review.service", Cursor: cursor})
					if logErr != nil {
						t.Fatal(logErr)
					}
					for _, entry := range logs.Entries {
						if entry.Stream == stream && entry.Message == "review-output-012345678901234567890123456789" {
							count++
						}
					}
					if !logs.More {
						break
					}
					if logs.Cursor == cursor {
						t.Fatal("log pagination did not advance")
					}
					cursor = logs.Cursor
				}
				want := 4
				if large {
					want = 5000
				}
				if count != want {
					t.Errorf("captured %d output lines, want %d", count, want)
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

func TestWindowsOneshotTimeoutAndExplicitStop(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprintf("explicit-stop=%t", explicit), func(t *testing.T) {
			body := "Type=oneshot\nTimeoutStartSec=1s\n"
			if explicit {
				body = "Type=oneshot\nTimeoutStartSec=0\n"
			}
			m := startWindowsHelperUnit(t, t.TempDir(), "waiting.service", body, "sleep", 0, "")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := m.Start(ctx, "waiting.service"); done <- err }()
			proc := waitWindowsLiveProc(t, m, "waiting.service")
			t.Cleanup(func() { _ = proc.Stop(3 * time.Second); _ = proc.Close() })
			if explicit {
				stopped := make(chan error, 1)
				go func() { _, err := m.Stop("waiting.service"); stopped <- err }()
				select {
				case err := <-stopped:
					if err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal("stop blocked on oneshot completion")
				}
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("interrupted oneshot reported success")
				}
			case <-ctx.Done():
				t.Fatal("oneshot start did not finish")
			}
			if proc.Alive() {
				t.Fatal("interrupted oneshot survived")
			}
		})
	}
}
