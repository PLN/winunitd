package manager

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/runtime"
)

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
