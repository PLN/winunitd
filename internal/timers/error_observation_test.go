package timers

import (
	"sync"
	"testing"
	"time"
)

type delayedStorageError struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (e *delayedStorageError) Error() string {
	e.once.Do(func() { close(e.entered) })
	<-e.release
	return "injected timer storage failure"
}

func TestTimerStorageErrorObservationDoesNotHoldDecisionLock(t *testing.T) {
	for _, phase := range []string{"load", "save"} {
		t.Run(phase, func(t *testing.T) {
			err := &delayedStorageError{entered: make(chan struct{}), release: make(chan struct{})}
			var once sync.Once
			release := func() { once.Do(func() { close(err.release) }) }
			store := &Store{}
			if phase == "load" {
				store.load = func(name string) (Runtime, error) {
					if name == "work.timer" {
						return Runtime{}, err
					}
					return Runtime{}, nil
				}
			} else {
				store.save = func(string, Runtime) error { return err }
			}
			clock := NewFake(time.Time{})
			e := NewEngine(clock.Clock(), store, nil)
			defer func() { release(); e.Stop() }()
			old := e.Arm(Spec{Name: "work.timer", OnStartupSec: time.Second, OnStartupSecSet: true})
			if phase == "save" {
				clock.Advance(2 * time.Second)
			}
			select {
			case <-err.entered:
			case <-time.After(time.Second):
				t.Fatal("storage error observation did not enter")
			}
			progress := make(chan struct{})
			go func() {
				e.Disarm("work.timer")
				e.Arm(Spec{Name: "other.timer", OnStartupSec: time.Hour, OnStartupSecSet: true})
				_ = e.Status("other.timer")
				close(progress)
			}()
			select {
			case <-progress:
			case <-time.After(time.Second):
				t.Fatal("storage error formatting held timer decision lock")
			}
			release()
			e.Stop()
			if e.Current("work.timer", old) || e.Status("other.timer").StorageError != "" {
				t.Fatal("stale storage error changed a different arm")
			}
		})
	}
}
