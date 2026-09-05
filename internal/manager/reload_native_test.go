package manager

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/runtime"
)

func TestReloadRetainsActiveNativeProxy(t *testing.T) {
	for _, kind := range []string{"scm", "scheduled-task"} {
		for _, change := range []string{"delete", "invalid"} {
			t.Run(kind+"/"+change, func(t *testing.T) {
				body := "[Service]\nType=scm\nServiceName=example-worker\n"
				var m *Manager
				var stopped func() bool
				if kind == "scm" {
					scm := newFakeSCM("example-worker")
					m = managerWithSCM(t, &fakeLauncher{}, scm, map[string]string{"worker.service": body})
					stopped = func() bool { s, err := scm.Query("example-worker"); return err == nil && s.State == runtime.SCMStopped }
				} else {
					body = "[Service]\nType=scheduled-task\nTaskName=example-worker\n"
					ts := newFakeTasks("example-worker")
					m = managerWithTasks(t, &fakeLauncher{}, ts, map[string]string{"worker.service": body})
					stopped = func() bool {
						s, err := ts.Query("example-worker")
						return err == nil && s.State == runtime.TaskReady && s.Instances == 0
					}
				}
				if _, err := m.Start(context.Background(), "worker"); err != nil {
					t.Fatal(err)
				}
				assertState(t, m, "worker.service", core.Active)
				path := filepath.Join(m.cfg.UnitsDir(), "worker.service")
				if change == "delete" {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				} else {
					writeUnit(t, m.cfg.UnitsDir(), "worker.service", "[Service]\nType=invalid\n")
				}
				if _, err := m.Reload(); err != nil {
					t.Fatal(err)
				}
				m.mu.Lock()
				rt := m.units["worker.service"]
				retained := rt != nil && rt.unavailable && rt.state == core.Active
				m.mu.Unlock()
				if !retained {
					t.Fatal("reload discarded active native proxy or its state")
				}
				status, err := m.Status("worker")
				if err != nil || status.Unit == nil || status.Unit.LoadState != "unavailable" || status.Unit.ActiveState != "active" {
					t.Fatalf("status after configuration loss = %+v, %v", status, err)
				}
				if _, err := m.Stop("worker"); err != nil {
					t.Fatalf("stop after configuration loss: %v", err)
				}
				assertState(t, m, "worker.service", core.Inactive)
				if !stopped() {
					t.Fatal("Stop did not reach the retained external service/task")
				}
				if _, err := m.Reload(); err != nil {
					t.Fatal(err)
				}
				m.mu.Lock()
				_, retained = m.units["worker.service"]
				m.mu.Unlock()
				if retained {
					t.Fatal("successfully stopped missing proxy remained retained")
				}
			})
		}
	}
}
