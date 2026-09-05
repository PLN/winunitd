package manager

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/runtime"
)

type reloadDelayedLauncher struct {
	fakeLauncher
	entered chan struct{}
	release chan struct{}
}

type reloadDelayedStopLauncher struct {
	fakeLauncher
	entered chan struct{}
	release chan struct{}
}

type reloadDelayedStopProcess struct {
	runtime.Process
	launcher *reloadDelayedStopLauncher
}

func (l *reloadDelayedStopLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	p, err := l.fakeLauncher.Start(ctx, spec)
	if err != nil {
		return nil, err
	}
	return &reloadDelayedStopProcess{Process: p, launcher: l}, nil
}

func (p *reloadDelayedStopProcess) Stop(timeout time.Duration) error {
	close(p.launcher.entered)
	<-p.launcher.release
	return p.Process.Stop(timeout)
}

func TestReloadKeepsStopInFlight(t *testing.T) {
	l := &reloadDelayedStopLauncher{entered: make(chan struct{}), release: make(chan struct{})}
	m := managerWith(t, l, map[string]string{"worker.service": "[Service]\nType=simple\nExecStart=C:\\Tools\\worker.exe\n"})
	var once sync.Once
	release := func() { once.Do(func() { close(l.release) }) }
	t.Cleanup(release)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := m.Start(ctx, "worker.service"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := m.Stop("worker.service"); done <- err }()
	select {
	case <-l.entered:
	case <-ctx.Done():
		t.Fatal("stop did not enter")
	}
	if err := os.Remove(filepath.Join(m.cfg.UnitsDir(), "worker.service")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Status("worker.service"); err != nil {
		t.Fatal("stop lost its runtime record", err)
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("stop did not finish")
	}
	status, err := m.Status("worker.service")
	if err != nil || status.Unit == nil || status.Unit.ActiveState != "inactive" {
		t.Fatalf("stop result lost: %+v, %v", status, err)
	}
}

func (l *reloadDelayedLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	close(l.entered)
	select {
	case <-l.release:
		return l.fakeLauncher.Start(ctx, spec)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestReloadKeepsLaunchInFlight(t *testing.T) {
	for _, restore := range []bool{false, true} {
		t.Run(map[bool]string{false: "deleted", true: "recreated"}[restore], func(t *testing.T) {
			launch := &reloadDelayedLauncher{entered: make(chan struct{}), release: make(chan struct{})}
			m := managerWith(t, launch, map[string]string{"worker.service": "[Service]\nType=simple\nExecStart=C:\\Tools\\worker.exe\n"})
			var once sync.Once
			release := func() { once.Do(func() { close(launch.release) }) }
			t.Cleanup(release)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := m.Start(ctx, "worker.service"); done <- err }()
			select {
			case <-launch.entered:
			case <-ctx.Done():
				t.Fatal("launch did not enter")
			}
			path := filepath.Join(m.cfg.UnitsDir(), "worker.service")
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if _, err = m.Reload(); err != nil {
				t.Fatal(err)
			}
			if _, err = m.Status("worker.service"); err != nil {
				t.Fatal("in-flight unit disappeared", err)
			}
			if restore {
				if err = os.WriteFile(path, original, 0600); err != nil {
					t.Fatal(err)
				}
				if _, err = m.Reload(); err != nil {
					t.Fatal(err)
				}
			}
			release()
			select {
			case err = <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("launch did not finish")
			}
			m.mu.Lock()
			proc := m.procOfLocked("worker.service")
			m.mu.Unlock()
			if proc == nil || !proc.Alive() {
				t.Fatal("late successful launch lost ownership")
			}
			if _, err = m.Stop("worker.service"); err != nil {
				t.Fatal(err)
			}
			if proc.Alive() {
				t.Fatal("stop left late process alive")
			}
		})
	}
}
