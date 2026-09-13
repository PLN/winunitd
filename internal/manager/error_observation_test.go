package manager

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/runtime"
)

type delayedLaunchError struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (e *delayedLaunchError) Error() string {
	e.once.Do(func() { close(e.entered) })
	<-e.release
	return "partial creation cleanup failed"
}

type delayedErrorLauncher struct {
	fakeLauncher
	err *delayedLaunchError
}

func (l *delayedErrorLauncher) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Process, error) {
	p, err := l.fakeLauncher.Start(ctx, spec)
	if err != nil {
		return p, err
	}
	return p, l.err
}

func TestLaunchErrorObservationPreservesIndependentProgress(t *testing.T) {
	e := &delayedLaunchError{entered: make(chan struct{}), release: make(chan struct{})}
	l := &delayedErrorLauncher{err: e}
	m := managerWith(t, l, map[string]string{
		"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n",
		"other.target": "[Unit]\nDescription=Independent\n",
	})
	var once sync.Once
	release := func() { once.Do(func() { close(e.release) }) }
	defer release()
	done := make(chan error, 1)
	go func() { _, err := m.Start(context.Background(), "work"); done <- err }()
	select {
	case <-e.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("error observation did not enter")
	}
	independent := make(chan error, 1)
	go func() {
		if _, err := m.Snapshot(); err != nil {
			independent <- err
			return
		}
		if _, err := m.Start(context.Background(), "other.target"); err != nil {
			independent <- err
			return
		}
		_, err := m.Stop("other.target")
		independent <- err
	}()
	select {
	case err := <-independent:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("error observation blocked snapshot and independent lifecycle")
	}
	release()
	if err := waitErr(t, done); err == nil {
		t.Fatal("partial creation reported success")
	}
	status, err := m.Status("work")
	if err != nil {
		t.Fatal(err)
	}
	if status.Unit.Error == "" {
		t.Fatal("accepted failure lost its diagnostic")
	}
	if _, err := m.Stop("work"); err != nil {
		t.Fatal(err)
	}
}

type delayedClassificationError struct{ delayedLaunchError }

func (e *delayedClassificationError) Error() string { return "adapter failed" }

func (e *delayedClassificationError) Is(target error) bool {
	if target == core.ErrSkipped {
		e.once.Do(func() { close(e.entered) })
		<-e.release
	}
	return false
}

type classificationErrorLauncher struct{ err error }

func (l classificationErrorLauncher) Start(context.Context, runtime.StartSpec) (runtime.Process, error) {
	return nil, l.err
}

func TestStartErrorClassificationPreservesCancellation(t *testing.T) {
	e := &delayedClassificationError{delayedLaunchError: delayedLaunchError{entered: make(chan struct{}), release: make(chan struct{})}}
	m := managerWith(t, classificationErrorLauncher{err: e}, map[string]string{
		"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n",
		"other.target": "[Unit]\nDescription=Independent\n",
	})
	var once sync.Once
	release := func() { once.Do(func() { close(e.release) }) }
	defer release()
	done := make(chan error, 1)
	go func() { _, err := m.Start(context.Background(), "work"); done <- err }()
	select {
	case <-e.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("start error classification did not enter")
	}
	var id string
	independent := make(chan error, 1)
	go func() {
		if _, err := m.Snapshot(); err != nil {
			independent <- err
			return
		}
		status, err := m.Status("work")
		if err != nil {
			independent <- err
			return
		}
		id = status.Unit.LastOperationID
		if _, err := m.CancelOperation(context.Background(), id); err != nil {
			independent <- err
			return
		}
		_, err = m.Start(context.Background(), "other.target")
		independent <- err
	}()
	select {
	case err := <-independent:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("error classifier blocked cancellation and independent lifecycle")
	}
	release()
	_ = waitErr(t, done) // cancellation may end the wait before final publication
	waitCond(t, func() bool { op, err := m.Operation(id); return err == nil && op.State == "failed" })
	op, err := m.Operation(id)
	if err != nil || !strings.Contains(op.Error, "adapter failed") || !strings.Contains(op.Error, "canceled by request") {
		t.Fatalf("completion lost error or concurrent cancellation: %+v %v", op, err)
	}
}
