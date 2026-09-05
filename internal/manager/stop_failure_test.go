package manager

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/runtime"
)

type failedStopLauncher struct {
	fakeLauncher
	proc *failedStopProcess
	lie  bool
}

type failedStopProcess struct {
	runtime.Process
	fail bool
	lie  bool
}

func (l *failedStopLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	p, err := l.fakeLauncher.Start(ctx, spec)
	if err != nil {
		return nil, err
	}
	l.proc = &failedStopProcess{Process: p, fail: true, lie: l.lie}
	return l.proc, nil
}

func (p *failedStopProcess) Stop(timeout time.Duration) error {
	if p.fail {
		if p.lie {
			return nil
		}
		return errors.New("injected termination failure")
	}
	return p.Process.Stop(timeout)
}

func TestFailedStopRetainsOwnershipAndRejectsStart(t *testing.T) {
	for _, lie := range []bool{false, true} {
		name := "failure"
		if lie {
			name = "success-with-live-process"
		}
		t.Run(name, func(t *testing.T) {
			l := &failedStopLauncher{lie: lie}
			m := managerWith(t, l, map[string]string{"worker.service": "[Service]\nType=simple\nExecStart=C:\\Tools\\worker.exe\n"})
			if _, err := m.Start(context.Background(), "worker.service"); err != nil {
				t.Fatal(err)
			}
			p := l.proc
			t.Cleanup(func() { p.fail = false; _ = p.Process.Stop(time.Second) })
			if _, err := m.Stop("worker.service"); err == nil {
				t.Error("unconfirmed termination reported success")
			}
			m.mu.Lock()
			owned := m.procOfLocked("worker.service") == p
			m.mu.Unlock()
			if !owned || !p.Alive() {
				t.Fatal("failed stop lost the live process")
			}
			if _, err := m.Start(context.Background(), "worker.service"); err == nil {
				t.Error("start admitted with uncertain termination")
			}
			if len(l.specs()) != 1 {
				t.Fatal("replacement launched after failed stop")
			}
			m.mu.Lock()
			retained := m.procOfLocked("worker.service") == p
			m.mu.Unlock()
			if !retained {
				t.Fatal("rejected start discarded uncertain process")
			}
			p.fail = false
			if _, err := m.Stop("worker.service"); err != nil {
				t.Fatal("stop retry failed", err)
			}
			if p.Alive() {
				t.Fatal("retry left process alive")
			}
		})
	}
}
