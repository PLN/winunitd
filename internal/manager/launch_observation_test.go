package manager

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/runtime"
)

func TestFailedReplacementStartPublishesFailure(t *testing.T) {
	launch := &failSecondRecoveryLauncher{}
	m := managerWith(t, launch, map[string]string{
		"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n",
	})
	if _, err := m.Start(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	old := m.units["work.service"].proc.(*fakeProc)
	m.mu.Unlock()
	// Liveness can become false before the old exit waiter is scheduled.
	// Keep its wait blocked until the replacement start cleans up the process.
	old.mu.Lock()
	old.dead, old.exited = true, true
	old.mu.Unlock()
	const cause = "injected fresh start failure"
	result, err := m.Start(context.Background(), "work")
	if err == nil || !strings.Contains(err.Error(), cause) {
		t.Fatalf("replacement error = %v, want %q", err, cause)
	}
	if result == nil || result.ActiveState != "failed" || !strings.Contains(result.Error, cause) {
		t.Errorf("replacement result = %+v", result)
	}
	st, err := m.Status("work")
	if err != nil {
		t.Fatal(err)
	}
	if st.Unit.ActiveState != "failed" || st.Unit.SubState != "" || st.Unit.MainPID != 0 || st.Unit.TerminationUncertain || !strings.Contains(st.Unit.Error, cause) {
		t.Fatalf("status after failed replacement = %+v", st.Unit)
	}
}

type delayedAliveLauncher struct {
	fakeLauncher
	process *delayedAliveProcess
}

type delayedAliveProcess struct {
	runtime.Process
	delay   atomic.Bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (l *delayedAliveLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	p, err := l.fakeLauncher.Start(ctx, spec)
	if err != nil {
		return nil, err
	}
	l.process = &delayedAliveProcess{Process: p, entered: make(chan struct{}), release: make(chan struct{})}
	return l.process, nil
}

func (p *delayedAliveProcess) Alive() bool {
	if p.delay.Load() {
		p.once.Do(func() { close(p.entered) })
		<-p.release
	}
	return p.Process.Alive()
}

func TestLaunchLivenessObservationDoesNotBlockIndependentAdmission(t *testing.T) {
	l := &delayedAliveLauncher{}
	m := managerWith(t, l, map[string]string{
		"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n",
		"other.target": "[Unit]\nDescription=Independent\n",
	})
	if _, err := m.Start(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release := func() { once.Do(func() { close(l.process.release) }) }
	defer release()
	l.process.delay.Store(true)
	done := make(chan error, 1)
	go func() { _, err := m.Start(context.Background(), "work"); done <- err }()
	select {
	case <-l.process.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("liveness observation did not enter")
	}
	independent := make(chan error, 1)
	go func() { _, err := m.Start(context.Background(), "other.target"); independent <- err }()
	select {
	case err := <-independent:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("liveness observation blocked independent admission")
	}
	release()
	if err := waitErr(t, done); err != nil {
		t.Fatal(err)
	}
	if len(l.units()) != 1 {
		t.Fatal("redundant start replaced the live invocation")
	}
}
