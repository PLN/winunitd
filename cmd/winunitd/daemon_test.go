package main

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/manager"
	"github.com/PLN/winunitd/internal/runtime"
)

const shutdownTestSID = "S-1-5-21-1-2-3-1001"

type finishUserProc struct {
	alive   atomic.Bool
	fail    atomic.Bool
	release <-chan struct{}
}

func (p *finishUserProc) PID() int                   { return 1 }
func (p *finishUserProc) SID() string                { return shutdownTestSID }
func (p *finishUserProc) Alive() bool                { return p.alive.Load() }
func (p *finishUserProc) Wait(context.Context) error { return nil }
func (p *finishUserProc) Kill() error {
	if p.release != nil {
		<-p.release
	}
	if p.fail.Load() {
		return errors.New("injected user cleanup failure")
	}
	p.alive.Store(false)
	return nil
}

func finishTestHost(p *finishUserProc) *manager.UserHost {
	h := manager.NewUserHost(manager.UserHostConfig{
		Admission:      manager.UserAdmission{Mode: "unit-files"},
		ProbeUserUnits: func(*runtime.UserToken) (bool, error) { return true, nil },
		Exe:            "winunitd-test",
		QueryToken: func(uint32) (*runtime.UserToken, error) {
			return &runtime.UserToken{Info: runtime.UserInfo{SID: shutdownTestSID}}, nil
		},
		Start: func(runtime.UserManagerSpec) (runtime.UserManagerProc, error) { return p, nil },
	})
	h.Logon(1)
	return h
}

func TestFinishReturnsUserCleanupFailure(t *testing.T) {
	p := &finishUserProc{}
	p.alive.Store(true)
	p.fail.Store(true)
	h := finishTestHost(p)
	t.Cleanup(func() { p.fail.Store(false); h.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := finishContext(ctx, nil, nil, h, io.Discard)
	if err == nil || runtime.IsCancellation(errors.Join(context.Canceled, err)) {
		t.Fatal("shutdown cleanup failure was suppressed")
	}
	if h.ManagerCount() != 1 {
		t.Fatal("shutdown failure lost user manager ownership")
	}
}

func TestFinishDeadlineRetainsUnclosedDaemonJob(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	p := &finishUserProc{release: release}
	p.alive.Store(true)
	h := finishTestHost(p)
	job, err := runtime.OpenDaemonJob()
	if err != nil {
		unblock()
		h.Close()
		t.Fatal(err)
	}
	previous := daemonJob
	daemonJob = job
	t.Cleanup(func() { unblock(); h.Close(); _ = job.Close(); daemonJob = previous })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- finishContext(ctx, nil, job, h, io.Discard) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("finish result: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("finish ignored shared deadline")
	}
	if daemonJob != job || job.Closed() {
		t.Fatal("deadline discarded the unclosed daemon job")
	}
	unblock()
	if err := finishContext(context.Background(), nil, job, h, io.Discard); err != nil {
		t.Fatal("finish retry", err)
	}
	if daemonJob != nil || !job.Closed() {
		t.Fatal("successful retry retained the daemon job")
	}
}
