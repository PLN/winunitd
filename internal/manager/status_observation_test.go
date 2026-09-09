package manager

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/protocol"
)

func TestStatusObservationDoesNotBlockIndependentStop(t *testing.T) {
	for _, list := range []bool{false, true} {
		t.Run(map[bool]string{false: "status", true: "list"}[list], func(t *testing.T) {
			l := &delayedAliveLauncher{}
			m := managerWith(t, l, map[string]string{
				"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n",
				"other.target": "[Unit]\nDescription=Independent\n",
			})
			for _, name := range []string{"work", "other.target"} {
				if _, err := m.Start(context.Background(), name); err != nil {
					t.Fatal(err)
				}
			}
			p := l.process
			var once sync.Once
			release := func() { once.Do(func() { close(p.release) }) }
			defer release()
			p.delay.Store(true)
			done := make(chan protocol.UnitStatus, 1)
			go func() {
				if list {
					result, _ := m.ListUnits()
					for _, st := range result.Units {
						if st.Name == "work.service" {
							done <- st
							return
						}
					}
				} else {
					result, _ := m.Status("work")
					done <- *result.Unit
				}
			}()
			select {
			case <-p.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("observation did not enter")
			}
			stopped := make(chan error, 1)
			go func() { _, err := m.Stop("other.target"); stopped <- err }()
			select {
			case err := <-stopped:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("status observation blocked independent stop")
			}
			release()
			select {
			case st := <-done:
				if st.MainPID != p.PID() || st.InvocationID == "" || st.ActiveState != "active" {
					t.Fatalf("captured status: %+v", st)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("status did not finish")
			}
		})
	}
}

func TestUserStatusObservationDoesNotBlockHostDecision(t *testing.T) {
	for _, list := range []bool{false, true} {
		t.Run(map[bool]string{false: "alive", true: "running"}[list], func(t *testing.T) {
			p := &gatedUserLiveness{entered: make(chan struct{}, 1), release: make(chan struct{})}
			p.alive.Store(true)
			p.block.Store(true)
			defer close(p.release)
			h := NewUserHost(UserHostConfig{})
			h.bySID[testSIDA] = &userInstance{sid: testSIDA, proc: p}
			done := make(chan struct{})
			go func() {
				if list {
					h.Running()
				} else {
					h.Alive(testSIDA)
				}
				close(done)
			}()
			select {
			case <-p.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("observation did not enter")
			}
			decision := make(chan struct{})
			go func() { h.acceptUserShutdown(); close(decision) }()
			select {
			case <-decision:
			case <-time.After(5 * time.Second):
				t.Fatal("observation blocked shutdown decision")
			}
			p.release <- struct{}{}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("observation did not finish")
			}
		})
	}
}
