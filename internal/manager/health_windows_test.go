//go:build windows

package manager

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestWindowsProbeHealthRetainsTransientFailure(t *testing.T) {
	requests := make(chan int, 8)
	gates := []chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1))
		select {
		case requests <- n:
		default:
		}
		if n >= 2 && n <= 4 {
			select {
			case <-gates[n-2]:
			case <-r.Context().Done():
				return
			}
		}
		if n == 2 {
			w.WriteHeader(200)
		} else {
			w.WriteHeader(503)
		}
	}))
	defer server.Close()
	body := fmt.Sprintf("[Unit]\nFormatVersion=2\n[Service]\nWatchdogMode=http\nWatchdogEndpoint=%s\nWatchdogSec=25ms\nWatchdogGraceSec=100ms\nWatchdogTimeoutSec=5s\nWatchdogFailureThreshold=2\nRestart=no\n", server.URL)
	m := startWindowsHelperUnit(t, t.TempDir(), "health.service", body, "sleep", 0, "")
	started := time.Now()
	if _, err := m.Start(context.Background(), "health.service"); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 4; n++ {
		select {
		case got := <-requests:
			if got != n {
				t.Fatal("unexpected probe sequence", got, n)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("native probe did not arrive")
		}
		if n == 1 {
			if time.Since(started) < 100*time.Millisecond {
				t.Fatal("native probe bypassed grace")
			}
			continue
		}
		want, failures := "degraded", 1
		if n == 3 {
			want, failures = "ready", 0
		}
		status, err := m.Status("health.service")
		if err != nil || status.Unit.Health != want || status.Unit.ProbeFailures != failures || status.Unit.MainPID == 0 || status.Unit.ActiveState != "active" {
			t.Fatalf("native transient health: %+v %v", status, err)
		}
		snapshot, err := m.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		u := snapshotUnit(t, snapshot, "health.service")
		if u.Health != want || u.ProbeFailures != failures || u.InvocationID != status.Unit.InvocationID {
			t.Fatal("snapshot lost native health identity")
		}
		close(gates[n-2])
	}
	waitUntil(t, 10*time.Second, func() bool {
		status, err := m.Status("health.service")
		return err == nil && status.Unit.Health == "unhealthy" && status.Unit.MainPID == 0 && len(status.Unit.PendingCleanup) == 0
	})
	if _, err := m.Stop("health.service"); err != nil {
		t.Fatal(err)
	}
	status, err := m.Status("health.service")
	if err != nil || status.Unit.Health != "unknown" || status.Unit.ProbeFailures != 0 {
		t.Fatal("explicit stop retained health claim", status, err)
	}
}
