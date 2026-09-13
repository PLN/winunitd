//go:build windows

package manager

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWindowsHTTPReadinessActivationAndCleanup(t *testing.T) {
	for _, action := range []string{"ready", "timeout", "stop"} {
		t.Run(action, func(t *testing.T) {
			entered := make(chan struct{}, 1)
			gate := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case entered <- struct{}{}:
				default:
				}
				select {
				case <-gate:
					w.WriteHeader(200)
				case <-r.Context().Done():
				}
			}))
			defer server.Close()
			timeout := "5s"
			if action == "timeout" {
				timeout = "150ms"
			}
			body := fmt.Sprintf("[Unit]\nFormatVersion=2\n[Service]\nReadinessMode=http\nReadinessEndpoint=%s\nReadinessIntervalSec=25ms\nReadinessTimeoutSec=5s\nTimeoutStartSec=%s\nRestart=no\n", server.URL, timeout)
			m := startWindowsHelperUnit(t, t.TempDir(), "ready.service", body, "sleep", 0, "")
			started := make(chan error, 1)
			go func() { _, err := m.Start(context.Background(), "ready.service"); started <- err }()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("native readiness probe missing")
			}
			if action != "timeout" {
				status, err := m.Status("ready.service")
				if err != nil || status.Unit.ActiveState != "activating" || status.Unit.MainPID == 0 || status.Unit.Health != "unknown" {
					t.Fatal("native process bypassed readiness", status, err)
				}
				select {
				case err := <-started:
					t.Fatal("startup finished before readiness", err)
				default:
				}
			}
			if action == "ready" {
				close(gate)
			} else if action == "stop" {
				if _, err := m.Stop("ready.service"); err != nil {
					t.Fatal(err)
				}
			}
			err := waitErr(t, started)
			if (err == nil) != (action == "ready") {
				t.Fatal("wrong native readiness outcome", action, err)
			}
			status, statusErr := m.Status("ready.service")
			if statusErr != nil {
				t.Fatal(statusErr)
			}
			if action == "ready" {
				if status.Unit.ActiveState != "active" || status.Unit.Health != "ready" {
					t.Fatal("native readiness not accepted", status)
				}
				snapshot, err := m.Snapshot()
				if err != nil {
					t.Fatal(err)
				}
				u := snapshotUnit(t, snapshot, "ready.service")
				if u.Health != "ready" || u.InvocationID != status.Unit.InvocationID || u.InvocationConfigRevision != status.Unit.InvocationConfigRevision {
					t.Fatal("snapshot lost readiness identity")
				}
			}
			if _, err := m.Stop("ready.service"); err != nil {
				t.Fatal(err)
			}
			status, statusErr = m.Status("ready.service")
			if statusErr != nil || status.Unit.MainPID != 0 || len(status.Unit.PendingCleanup) != 0 {
				t.Fatal("native readiness cleanup pending", status, statusErr)
			}
		})
	}
}

func TestWindowsTCPReadinessAcceptsLoopbackConnection(t *testing.T) {
	_, addr := listenLoopbackTCP(t)
	body := fmt.Sprintf("[Unit]\nFormatVersion=2\n[Service]\nReadinessMode=tcp\nReadinessEndpoint=%s\nTimeoutStartSec=5s\n", addr)
	m := startWindowsHelperUnit(t, t.TempDir(), "tcp-ready.service", body, "sleep", 0, "")
	if _, err := m.Start(context.Background(), "tcp-ready.service"); err != nil {
		t.Fatal(err)
	}
	status, err := m.Status("tcp-ready.service")
	if err != nil || status.Unit.Health != "ready" || status.Unit.ActiveState != "active" || status.Unit.MainPID == 0 {
		t.Fatal("native TCP readiness not accepted", status, err)
	}
	if _, err := m.Stop("tcp-ready.service"); err != nil {
		t.Fatal(err)
	}
}
