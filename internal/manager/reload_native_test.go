package manager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

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

func TestReloadRetargetKeepsNativeOwnership(t *testing.T) {
	for _, kind := range []string{"scm", "scheduled-task"} {
		t.Run(kind, func(t *testing.T) {
			var m *Manager
			var active func(string) bool
			field := "ServiceName"
			if kind == "scheduled-task" {
				field = "TaskName"
			}
			definition := func(target string) string {
				return "[Service]\nType=" + kind + "\n" + field + "=" + target + "\n"
			}
			if kind == "scm" {
				scm := newFakeSCM("example-old", "example-new")
				m = managerWithSCM(t, &fakeLauncher{}, scm, map[string]string{"worker.service": definition("example-old")})
				active = func(name string) bool { s, _ := scm.Query(name); return s.State == runtime.SCMRunning }
			} else {
				tasks := newFakeTasks("example-old", "example-new")
				m = managerWithTasks(t, &fakeLauncher{}, tasks, map[string]string{"worker.service": definition("example-old")})
				active = func(name string) bool { s, _ := tasks.Query(name); return s.Instances > 0 }
			}
			if _, err := m.Start(context.Background(), "worker"); err != nil {
				t.Fatal(err)
			}
			writeUnit(t, m.cfg.UnitsDir(), "worker.service", definition("example-new"))
			if _, err := m.Reload(); err != nil {
				t.Fatal(err)
			}
			status, err := m.Status("worker")
			if err != nil || status.Unit.ActiveState != "active" {
				t.Fatalf("reload redirected status away from the owned resource: %+v, %v", status, err)
			}
			if _, err := m.Stop("worker"); err != nil {
				t.Fatal(err)
			}
			if active("example-old") || active("example-new") {
				t.Fatal("stop did not resolve original ownership")
			}
			if _, err := m.Start(context.Background(), "worker"); err != nil {
				t.Fatal(err)
			}
			if active("example-old") || !active("example-new") {
				t.Fatal("fresh start did not adopt the new definition")
			}
		})
	}
}

func TestReloadServiceTypeKeepsOwnership(t *testing.T) {
	definitions := map[string]string{
		"process": "[Service]\nType=simple\nExecStart=C:\\Tools\\worker.exe\n",
		"scm":     "[Service]\nType=scm\nServiceName=example-worker\n",
		"task":    "[Service]\nType=scheduled-task\nTaskName=example-worker\n",
	}
	for oldKind, oldDefinition := range definitions {
		for newKind, newDefinition := range definitions {
			if oldKind == newKind {
				continue
			}
			for _, shutdown := range []bool{false, true} {
				label := oldKind + "-to-" + newKind
				if shutdown {
					label += "/shutdown"
				}
				t.Run(label, func(t *testing.T) {
					launch := &fakeLauncher{}
					scm := newFakeSCM("example-worker")
					tasks := newFakeTasks("example-worker")
					m := managerWithSCM(t, launch, scm, map[string]string{"worker.service": oldDefinition})
					m.tasks = tasks
					if _, err := m.Start(context.Background(), "worker"); err != nil {
						t.Fatal(err)
					}
					writeUnit(t, m.cfg.UnitsDir(), "worker.service", newDefinition)
					if _, err := m.Reload(); err != nil {
						t.Fatal(err)
					}
					list, err := m.ListUnits()
					if err != nil {
						t.Fatal(err)
					}
					found := false
					for _, status := range list.Units {
						if status.Name == "worker.service" {
							found = true
							if status.ActiveState != "active" {
								t.Fatalf("lost old ownership in list: %+v", status)
							}
						}
					}
					if !found {
						t.Fatal("worker missing from list")
					}
					if shutdown {
						if err := m.Shutdown(context.Background()); err != nil {
							t.Fatal(err)
						}
					} else {
						if _, err := m.Stop("worker"); err != nil {
							t.Fatal(err)
						}
					}
					ss, _ := scm.Query("example-worker")
					ts, _ := tasks.Query("example-worker")
					if ss.State == runtime.SCMRunning || ts.Instances > 0 {
						t.Fatal("old native resource survived stop")
					}
					if oldKind == "process" && len(launch.stopped()) != 1 {
						t.Fatal("old process was not stopped")
					}
					if !shutdown {
						if _, err := m.Start(context.Background(), "worker"); err != nil {
							t.Fatal(err)
						}
						ss, _ = scm.Query("example-worker")
						ts, _ = tasks.Query("example-worker")
						if (newKind == "scm") != (ss.State == runtime.SCMRunning) || (newKind == "task") != (ts.Instances > 0) {
							t.Fatal("fresh start did not adopt new type")
						}
						if newKind == "process" && len(launch.specs()) != 1 {
							t.Fatal("fresh start did not launch process")
						}
					}
				})
			}
		}
	}
}

func TestReloadDoesNotChangeAutomaticRecoveryDefinition(t *testing.T) {
	launch := &fakeLauncher{}
	old := "[Service]\nType=simple\nExecStart=C:\\Tools\\old.exe\nRestart=always\nRestartSec=5s\n"
	m, clock := managerWithFake(t, launch, map[string]string{"worker.service": old})
	if _, err := m.Start(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	proc := m.units["worker.service"].proc.(*fakeProc)
	m.mu.Unlock()
	writeUnit(t, m.cfg.UnitsDir(), "worker.service", "[Service]\nType=simple\nExecStart=C:\\Tools\\new.exe\nRestart=no\n")
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	proc.die(1)
	waitSub(t, m, "worker.service", core.SubAutoRestart)
	advanceWait(t, clock, 5*time.Second)
	waitCond(t, func() bool { return len(launch.specs()) >= 2 })
	if got := launch.specs()[1].Argv[0]; got != `C:\Tools\old.exe` {
		t.Fatalf("automatic recovery launched %q", got)
	}
	if _, err := m.Stop("worker"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	if got := launch.specs()[2].Argv[0]; got != `C:\Tools\new.exe` {
		t.Fatalf("explicit start launched %q", got)
	}
}

// Model adapters that fail after a native side effect, then fail their first
// cleanup attempt. Neither result permits replacing the old resource identity.
type uncertainReloadSCM struct {
	runtime.SCM
	failStop bool
}

func (s *uncertainReloadSCM) Start(ctx context.Context, name string, timeout time.Duration) (runtime.SCMStatus, error) {
	status, err := s.SCM.Start(ctx, name, timeout)
	if err != nil {
		return status, err
	}
	if name == "example-old" {
		return status, errors.New("injected failure after start")
	}
	return status, nil
}

func (s *uncertainReloadSCM) Stop(ctx context.Context, name string, timeout time.Duration) (runtime.SCMStatus, error) {
	if s.failStop {
		s.failStop = false
		return runtime.SCMStatus{}, errors.New("injected stop failure")
	}
	return s.SCM.Stop(ctx, name, timeout)
}

func TestReloadRetargetRetainsFailedNativeOwnership(t *testing.T) {
	native := newFakeSCM("example-old", "example-new")
	adapter := &uncertainReloadSCM{SCM: native, failStop: true}
	m := managerWithSCM(t, &fakeLauncher{}, adapter, map[string]string{
		"worker.service": "[Service]\nType=scm\nServiceName=example-old\n",
	})
	if _, err := m.Start(context.Background(), "worker"); err == nil {
		t.Fatal("injected start failure missing")
	}
	writeUnit(t, m.cfg.UnitsDir(), "worker.service", "[Service]\nType=scm\nServiceName=example-new\n")
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	if err := m.launchUnit(context.Background(), "worker.service", false); err == nil {
		t.Fatal("retargeted start abandoned uncertain native ownership")
	}
	if _, err := m.Stop("worker"); err == nil {
		t.Fatal("injected stop failure missing")
	}
	if err := m.launchUnit(context.Background(), "worker.service", false); err == nil {
		t.Fatal("start bypassed failed cleanup")
	}
	status, err := m.Status("worker")
	if err != nil || status.Unit.ActiveState != "active" {
		t.Fatalf("status lost failed ownership: %+v, %v", status, err)
	}
	if _, err := m.Stop("worker"); err != nil {
		t.Fatal(err)
	}
	old, _ := native.Query("example-old")
	if old.State != runtime.SCMStopped {
		t.Fatal("retry did not stop old target")
	}
	if _, err := m.Start(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	fresh, _ := native.Query("example-new")
	if fresh.State != runtime.SCMRunning {
		t.Fatal("new target was not adopted after cleanup")
	}
}

type pausedReloadSCM struct {
	runtime.SCM
	entered chan struct{}
	release chan struct{}
}

func (s *pausedReloadSCM) Start(ctx context.Context, name string, timeout time.Duration) (runtime.SCMStatus, error) {
	close(s.entered)
	<-s.release
	return s.SCM.Start(ctx, name, timeout)
}

func TestReloadDuringNativeLaunchKeepsOwnership(t *testing.T) {
	native := newFakeSCM("example-old", "example-new")
	adapter := &pausedReloadSCM{SCM: native, entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(adapter.release) }) }
	m := managerWithSCM(t, &fakeLauncher{}, adapter, map[string]string{
		"worker.service": "[Service]\nType=scm\nServiceName=example-old\n",
	})
	defer release()
	done := make(chan error, 1)
	go func() { _, err := m.Start(context.Background(), "worker"); done <- err }()
	select {
	case <-adapter.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("native start did not arrive")
	}
	writeUnit(t, m.cfg.UnitsDir(), "worker.service", "[Service]\nType=scm\nServiceName=example-new\n")
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("native start did not finish")
	}
	if _, err := m.Stop("worker"); err != nil {
		t.Fatal(err)
	}
	old, _ := native.Query("example-old")
	fresh, _ := native.Query("example-new")
	if old.State != runtime.SCMStopped || fresh.State != runtime.SCMStopped {
		t.Fatal("reload during launch lost native ownership")
	}
}
