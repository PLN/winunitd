//go:build windows

package manager

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
)

type exitedBeforeAttachLauncher struct {
	runtime.Launcher
	observed chan runtime.Process
}

func (l *exitedBeforeAttachLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	p, err := l.Launcher.Start(ctx, spec)
	if err != nil {
		return p, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err = p.Wait(waitCtx)
	l.observed <- p
	var exit *runtime.ExitStatus
	if err != nil && !errors.As(err, &exit) {
		return p, err
	}
	// Return the real owner only after its creation handle observed native exit.
	// Small output fits in the pipe before manager capture is attached.
	return p, nil
}

func TestWindowsExitBeforeManagerAttachPreservesResultAndOutput(t *testing.T) {
	for _, tc := range []struct {
		name, kind, helper, state string
		exit                      int
	}{
		{"oneshot-success", "oneshot", "review-output", "inactive", 0},
		{"oneshot-failure", "oneshot", "exit", "failed", 7},
		{"simple-failure", "simple", "exit", "failed", 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			body := fmt.Sprintf("Type=%s\nTimeoutStartSec=10s\nRestart=no\nEnvironment=\"WINUNITD_REVIEW_PID_FILE=%s\"\n", tc.kind, filepath.Join(dir, "fixture.pid"))
			m := startWindowsHelperUnit(t, dir, "early.service", body, tc.helper, tc.exit, "")
			l := &exitedBeforeAttachLauncher{Launcher: m.launch, observed: make(chan runtime.Process, 1)}
			m.launch = l
			_, startErr := m.Start(context.Background(), "early.service")
			if tc.kind == "oneshot" && (startErr != nil) != (tc.exit != 0) {
				t.Fatalf("oneshot result: %v", startErr)
			}
			var p runtime.Process
			select {
			case p = <-l.observed:
			case <-time.After(time.Second):
				t.Fatal("launcher did not observe native exit")
			}
			waitUntil(t, 5*time.Second, func() bool {
				st, err := m.Status("early.service")
				return err == nil && st.Unit.ActiveState == tc.state && st.Unit.MainPID == 0 && len(st.Unit.PendingCleanup) == 0
			})
			st, err := m.Status("early.service")
			if err != nil || st.Unit.InvocationID == "" || st.Unit.InvocationConfigRevision == "" || (tc.exit != 0 && st.Unit.Error == "") {
				t.Fatalf("early exit lost invocation/result: %+v %v", st, err)
			}
			if tc.helper == "review-output" {
				logs, err := m.Logs(protocol.LogsParams{Unit: "early.service"})
				if err != nil {
					t.Fatal(err)
				}
				count := 0
				for _, entry := range logs.Entries {
					if entry.Stream == "stdout" {
						count++
					}
				}
				if count != 4 {
					t.Fatalf("early-exit output records=%d, want 4", count)
				}
			}
			if _, err := m.Stop("early.service"); err != nil {
				t.Fatal(err)
			}
			if p.Alive() {
				t.Fatal("early-exit process remained live")
			}
		})
	}
}
