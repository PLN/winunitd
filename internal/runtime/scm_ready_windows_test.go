//go:build windows

package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
)

type serviceExit struct {
	specific bool
	code     uint32
}

func startReadyHost(t *testing.T, run func(context.Context, func()) error) (chan svc.ChangeRequest, chan svc.Status, <-chan serviceExit) {
	t.Helper()
	requests := make(chan svc.ChangeRequest, 4)
	changes := make(chan svc.Status, 32)
	done := make(chan serviceExit, 1)
	h := &host{runReady: run}
	go func() { specific, code := h.Execute(nil, requests, changes); done <- serviceExit{specific, code} }()
	t.Cleanup(func() {
		select {
		case requests <- svc.ChangeRequest{Cmd: svc.Stop}:
		default:
		}
	})
	return requests, changes, done
}

func nextServiceStatus(t *testing.T, changes <-chan svc.Status) svc.Status {
	t.Helper()
	select {
	case status := <-changes:
		return status
	case <-time.After(3 * time.Second):
		t.Fatal("missing SCM status")
		return svc.Status{}
	}
}

func TestHostReadinessAndAuthoritativeStartupStatus(t *testing.T) {
	old := startPendingTick
	startPendingTick = 20 * time.Millisecond
	t.Cleanup(func() { startPendingTick = old })
	callback := make(chan func(), 1)
	requests, changes, done := startReadyHost(t, func(ctx context.Context, ready func()) error {
		callback <- ready
		<-ctx.Done()
		return ctx.Err()
	})
	first := nextServiceStatus(t, changes)
	if first.State != svc.StartPending || first.Accepts != 0 || first.WaitHint == 0 {
		t.Fatal("invalid initial status", first)
	}
	ready := <-callback
	progress := nextServiceStatus(t, changes)
	if progress.State != svc.StartPending || progress.CheckPoint <= first.CheckPoint {
		t.Fatal("startup readiness was not awaited", progress)
	}
	// An old SCM snapshot must not publish Running before our readiness event.
	requests <- svc.ChangeRequest{Cmd: svc.Interrogate, CurrentStatus: svc.Status{State: svc.Running}}
	if status := nextServiceStatus(t, changes); status.State != svc.StartPending {
		t.Fatal("interrogate promoted startup", status)
	}
	ready()
	ready()
	for {
		status := nextServiceStatus(t, changes)
		if status.State == svc.Running {
			break
		}
		if status.State != svc.StartPending {
			t.Fatal("unexpected readiness status", status)
		}
	}
	requests <- svc.ChangeRequest{Cmd: svc.Stop}
	select {
	case exit := <-done:
		if exit.specific || exit.code != 0 {
			t.Fatal("ordered stop failed", exit)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("service did not stop")
	}
}

func TestHostInitializationFailureNeverReportsRunning(t *testing.T) {
	for _, result := range []error{nil, errors.New("listener initialization failed")} {
		_, changes, done := startReadyHost(t, func(context.Context, func()) error { return result })
		select {
		case exit := <-done:
			if !exit.specific || exit.code == 0 {
				t.Fatal("initialization exit reported success", exit)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("initialization failure did not exit")
		}
		for len(changes) > 0 {
			if status := <-changes; status.State == svc.Running {
				t.Fatal("failed initialization reported Running")
			}
		}
	}
}

func TestHostStopDuringInitializationRejectsLateReady(t *testing.T) {
	requests, changes, done := startReadyHost(t, func(ctx context.Context, ready func()) error {
		<-ctx.Done()
		ready()
		return ctx.Err()
	})
	if status := nextServiceStatus(t, changes); status.State != svc.StartPending {
		t.Fatal(status)
	}
	requests <- svc.ChangeRequest{Cmd: svc.Stop}
	select {
	case exit := <-done:
		if exit.specific || exit.code != 0 {
			t.Fatal("startup stop failed", exit)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("startup stop did not complete")
	}
	for len(changes) > 0 {
		if status := <-changes; status.State == svc.Running {
			t.Fatal("late readiness resurrected Running")
		}
	}
}
