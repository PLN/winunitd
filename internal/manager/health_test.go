package manager

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
)

func healthUnit(endpoint string) string {
	return fmt.Sprintf("[Unit]\nFormatVersion=2\n[Service]\nExecStart=C:\\Apps\\worker.exe\nWatchdogMode=http\nWatchdogEndpoint=%s\nWatchdogSec=1s\nWatchdogGraceSec=5s\nWatchdogTimeoutSec=100ms\nWatchdogFailureThreshold=2\nRestart=no\n", endpoint)
}

func TestProbeHealthGraceThresholdRecoveryAndCapturedPolicy(t *testing.T) {
	var code, calls atomic.Int32
	code.Store(503)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(int(code.Load())) }))
	defer server.Close()
	body := healthUnit(server.URL)
	m, clock := managerWithFake(t, &fakeLauncher{}, map[string]string{"work.service": body})
	if _, err := m.Start(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	rt := m.units["work.service"]
	owner := runtimeIdentity{name: "work.service", record: rt, gen: rt.gen}
	m.mu.Unlock()
	waitCond(t, func() bool { return clock.WaitingAt(5 * time.Second) })
	if calls.Load() != 0 {
		t.Fatal("probe ran before grace")
	}
	for i, step := range []struct {
		code     int32
		advance  time.Duration
		health   string
		failures int
	}{{503, 5 * time.Second, "degraded", 1}, {200, time.Second, "ready", 0}, {503, time.Second, "degraded", 1}, {503, time.Second, "unhealthy", 2}} {
		code.Store(step.code)
		clock.Advance(step.advance)
		waitCond(t, func() bool {
			st, err := m.Status("work")
			return err == nil && st.Unit.Health == step.health && st.Unit.ProbeFailures == step.failures
		})
		snapshot, err := m.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		u := snapshotUnit(t, snapshot, "work.service")
		if u.Health != step.health || u.ProbeFailures != step.failures {
			t.Fatal("snapshot lost accepted health")
		}
		if i < 3 {
			status, err := m.Status("work")
			if err != nil || status.Unit.ActiveState != "active" || status.Unit.MainPID == 0 {
				t.Fatal("transient failure stopped process", status, err)
			}
			waitCond(t, func() bool { return clock.WaitingAt(time.Second) })
		}
		if i == 0 {
			writeUnit(t, m.cfg.UnitsDir(), "work.service", strings.Replace(body, "WatchdogFailureThreshold=2", "WatchdogFailureThreshold=1", 1))
			if result, err := m.Reload(); err != nil || len(result.Errors) != 0 {
				t.Fatalf("reload: %+v %v", result, err)
			}
		}
	}
	waitWatchdogFailed(t, m, "work.service")
	if calls.Load() != 4 {
		t.Fatal("unexpected probe count", calls.Load())
	}
	if _, err := m.Stop("work"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	if accepted, _ := m.acceptProbeHealth(owner, false, 1); accepted {
		t.Fatal("old probe changed replacement")
	}
	status, err := m.Status("work")
	if err != nil || status.Unit.Health != "unknown" || status.Unit.ProbeFailures != 0 {
		t.Fatal("replacement inherited old health", status, err)
	}
	waitCond(t, func() bool { return clock.WaitingAt(5 * time.Second) })
	clock.Advance(5 * time.Second)
	waitWatchdogFailed(t, m, "work.service") // explicit launch adopted threshold 1
}

func TestProbeHealthDeadlineRejectsHungResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	body := strings.Replace(healthUnit(server.URL), "WatchdogFailureThreshold=2", "WatchdogFailureThreshold=1", 1)
	m, clock := managerWithFake(t, &fakeLauncher{}, map[string]string{"work.service": body})
	if _, err := m.Start(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	waitCond(t, func() bool { return clock.WaitingAt(5 * time.Second) })
	clock.Advance(5 * time.Second)
	waitWatchdogFailed(t, m, "work.service")
	status, err := m.Status("work")
	if err != nil || status.Unit.Health != "unhealthy" || status.Unit.SubState != core.SubWatchdog.String() {
		t.Fatal("timed-out probe reported healthy", status, err)
	}
}
