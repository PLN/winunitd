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
)

func readinessUnit(endpoint string) string {
	return fmt.Sprintf("[Unit]\nFormatVersion=2\n[Service]\nExecStart=C:\\Apps\\worker.exe\nReadinessMode=http\nReadinessEndpoint=%s\nReadinessIntervalSec=100ms\nReadinessTimeoutSec=1s\nTimeoutStartSec=2s\nRestart=no\n", endpoint)
}

func TestHTTPReadinessOrdersDependentsAndKeepsCapturedPolicy(t *testing.T) {
	var calls, newCalls atomic.Int32
	gate := make(chan struct{})
	entered := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		entered <- struct{}{}
		if n == 1 {
			w.WriteHeader(503)
			return
		}
		select {
		case <-gate:
			w.WriteHeader(200)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	replacement := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { newCalls.Add(1); w.WriteHeader(200) }))
	defer replacement.Close()
	launch := &fakeLauncher{}
	body := readinessUnit(server.URL)
	m, clock := managerWithFake(t, launch, map[string]string{
		"app.service":    body,
		"client.service": "[Unit]\nRequires=app.service\nAfter=app.service\n[Service]\nExecStart=C:\\Apps\\client.exe\n",
	})
	started := make(chan error, 1)
	go func() { _, err := m.Start(context.Background(), "client"); started <- err }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("readiness probe did not arrive")
	}
	waitCond(t, func() bool { return clock.WaitingAt(100 * time.Millisecond) })
	status, err := m.Status("app")
	if err != nil || status.Unit.ActiveState != "activating" || status.Unit.Health != "unknown" || status.Unit.MainPID == 0 {
		t.Fatal("startup readiness was not pending", status, err)
	}
	if len(launch.specs()) != 1 {
		t.Fatal("dependent bypassed readiness")
	}
	writeUnit(t, m.cfg.UnitsDir(), "app.service", strings.Replace(body, server.URL, replacement.URL, 1))
	if result, err := m.Reload(); err != nil || len(result.Errors) != 0 {
		t.Fatalf("reload: %+v %v", result, err)
	}
	clock.Advance(100 * time.Millisecond)
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("second captured probe did not arrive")
	}
	select {
	case err := <-started:
		t.Fatal("start completed before readiness", err)
	default:
	}
	if newCalls.Load() != 0 {
		t.Fatal("reload changed in-flight probe target")
	}
	close(gate)
	if err := waitErr(t, started); err != nil {
		t.Fatal(err)
	}
	status, err = m.Status("app")
	if err != nil || status.Unit.Health != "ready" || status.Unit.ActiveState != "active" {
		t.Fatal("readiness success was not accepted", status, err)
	}
	if len(launch.specs()) != 2 {
		t.Fatal("dependent did not start after readiness")
	}
	if _, err := m.Stop("app"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(context.Background(), "app"); err != nil {
		t.Fatal(err)
	}
	if newCalls.Load() != 1 {
		t.Fatal("fresh activation did not adopt replacement readiness")
	}
}

func TestHTTPReadinessStopDeadlineAndExitRetainCleanup(t *testing.T) {
	for _, action := range []string{"stop", "deadline", "exit"} {
		t.Run(action, func(t *testing.T) {
			entered := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case entered <- struct{}{}:
				default:
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			m, clock := managerWithFake(t, &fakeLauncher{}, map[string]string{"work.service": readinessUnit(server.URL)})
			started := make(chan error, 1)
			go func() { _, err := m.Start(context.Background(), "work"); started <- err }()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("held readiness probe missing")
			}
			m.mu.Lock()
			p := m.units["work.service"].proc.(*fakeProc)
			m.mu.Unlock()
			switch action {
			case "stop":
				if _, err := m.Stop("work"); err != nil {
					t.Fatal(err)
				}
			case "deadline":
				waitCond(t, func() bool { return clock.WaitingAt(2 * time.Second) })
				clock.Advance(2 * time.Second)
			case "exit":
				p.die(7)
			}
			if err := waitErr(t, started); err == nil {
				t.Fatal("interrupted readiness reported success")
			}
			if p.Alive() {
				t.Fatal("interrupted readiness leaked process")
			}
			if _, err := m.Stop("work"); err != nil {
				t.Fatal(err)
			}
			status, err := m.Status("work")
			if err != nil || status.Unit.MainPID != 0 || len(status.Unit.PendingCleanup) != 0 {
				t.Fatal("readiness cleanup remained owned", status, err)
			}
		})
	}
}
