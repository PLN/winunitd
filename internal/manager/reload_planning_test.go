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
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/unit"
)

func TestReloadRevalidatesOwnershipAfterUnlockedPlanning(t *testing.T) {
	for _, mode := range []string{"new ownership", "completed cleanup", "stale build error"} {
		t.Run(mode, func(t *testing.T) {
			m := testManager(t, map[string]string{"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n"})
			if mode == "completed cleanup" {
				if _, err := m.Start(context.Background(), "work"); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Remove(filepath.Join(m.cfg.UnitsDir(), "work.service")); err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			attempts := 0
			done := make(chan error, 1)
			go func() {
				_, err := m.reloadWithBuilder(func(units []*unit.Unit) (*core.Graph, error) {
					attempts++
					if attempts == 1 {
						close(entered)
						<-release
						if mode == "stale build error" {
							return nil, errors.New("obsolete candidate error")
						}
					}
					return core.Build(units)
				})
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("planner did not enter")
			}
			changed := make(chan error, 1)
			go func() {
				var err error
				if mode == "completed cleanup" {
					_, err = m.Stop("work")
				} else {
					_, err = m.Start(context.Background(), "work")
				}
				changed <- err
			}()
			select {
			case err := <-changed:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("graph planning blocked lifecycle work")
			}
			unblock()
			if err := waitErr(t, done); err != nil {
				t.Fatal(err)
			}
			if attempts != 2 {
				t.Fatalf("build attempts=%d, want 2", attempts)
			}
			status, err := m.Status("work")
			if mode == "completed cleanup" {
				var rpcErr *protocol.Error
				if !errors.As(err, &rpcErr) || rpcErr.Code != protocol.CodeNotFound {
					t.Fatalf("dropped unit status: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if status.Unit.LoadState != "unavailable" || status.Unit.MainPID == 0 {
					t.Fatalf("retained unit: %+v", status.Unit)
				}
				if _, err := m.Stop("work"); err != nil {
					t.Fatalf("retained stop routing: %v", err)
				}
			}
		})
	}
}

func TestReloadOwnershipChurnRejectsWithoutPublication(t *testing.T) {
	m := testManager(t, map[string]string{"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n"})
	if err := os.Remove(filepath.Join(m.cfg.UnitsDir(), "work.service")); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	revision, graph := m.configRevision, m.graph
	m.mu.Unlock()
	attempts := 0
	_, err := m.reloadWithBuilder(func(units []*unit.Unit) (*core.Graph, error) {
		attempts++
		m.mu.Lock()
		rt := m.units["work.service"]
		rt.stopUncertain = !rt.stopUncertain
		m.mu.Unlock()
		return core.Build(units)
	})
	var rpcErr *protocol.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != protocol.CodeBusy || attempts != 3 {
		t.Fatalf("attempts=%d error=%v", attempts, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.units["work.service"].stopUncertain = false
	if m.configRevision != revision || m.graph != graph || m.units["work.service"].unavailable {
		t.Fatal("rejected candidate changed accepted configuration")
	}
}

func TestEnabledGraphPlanningDoesNotBlockStop(t *testing.T) {
	m := testManager(t, map[string]string{"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n"})
	if _, err := m.Start(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	done := make(chan error, 1)
	go func() {
		m.configMu.Lock()
		defer m.configMu.Unlock()
		done <- m.rebuildGraphWithLinks(map[string][]string{DefaultTarget: {"work.service"}}, func(units []*unit.Unit) (*core.Graph, error) {
			close(entered)
			<-release
			return core.Build(units)
		})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("planner did not enter")
	}
	stopped := make(chan error, 1)
	go func() { _, err := m.Stop("work"); stopped <- err }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("enable graph planning blocked stop")
	}
	unblock()
	if err := waitErr(t, done); err != nil {
		t.Fatal(err)
	}
	status, err := m.Status("work")
	if err != nil {
		t.Fatal(err)
	}
	if !status.Unit.Enabled || status.Unit.ActiveState != "inactive" {
		t.Fatalf("enable publication changed stop outcome: %+v", status.Unit)
	}
}
