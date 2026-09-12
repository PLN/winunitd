package manager

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/runtime"
)

type barrierUserProc struct {
	fakeUserMgr
	check func()
}

func TestShutdownAllDeadlineJoinsBlockedDecision(t *testing.T) {
	h := NewUserHost(UserHostConfig{})
	h.mu.Lock()
	var once sync.Once
	release := func() { once.Do(h.mu.Unlock) }
	defer release()
	var first *stopAttempt
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		err := ShutdownAll(ctx, nil, h)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("blocked decision ignored caller deadline: %v", err)
		}
		h.stops.mu.Lock()
		pending := h.stops.pending[stopKey{allUsers: h}]
		h.stops.mu.Unlock()
		if pending == nil {
			t.Fatal("blocked combined pass lost ownership")
		}
		if first == nil {
			first = pending
		} else if first != pending {
			t.Fatal("retry duplicated blocked combined pass")
		}
	}
	release()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := ShutdownAll(ctx, nil, h); err != nil {
		t.Fatal("fresh retry inherited old deadline", err)
	}
}

func (p *barrierUserProc) Kill() error {
	p.check()
	return p.fakeUserMgr.Kill()
}

func TestShutdownAllSealsBothDomainsBeforeCleanup(t *testing.T) {
	const name = "work.service"
	m := managerWith(t, &fakeLauncher{}, map[string]string{name: "[Service]\nExecStart=C:\\Tools\\worker.exe\n"})
	h, _, _ := testUserHost(t, map[uint32]string{1: testSIDA}, nil)
	var checked atomic.Bool
	p := &barrierUserProc{check: func() {
		m.mu.Lock()
		systemClosed := m.closed
		m.mu.Unlock()
		h.mu.Lock()
		userClosed := h.closed
		h.mu.Unlock()
		if !systemClosed || !userClosed {
			t.Error("cleanup preceded the combined admission barrier")
		}
		checked.Store(true)
	}}
	p.sid = testSIDA
	p.alive.Store(true)
	h.cfg.Start = func(runtime.UserManagerSpec) (runtime.UserManagerProc, error) { return p, nil }
	h.Logon(1)
	if _, err := m.Start(context.Background(), name); err != nil {
		t.Fatal(err)
	}
	if err := ShutdownAll(context.Background(), m, h); err != nil {
		t.Fatal(err)
	}
	if !checked.Load() || p.Alive() || h.ManagerCount() != 0 {
		t.Fatal("user cleanup incomplete")
	}
	if _, err := m.Start(context.Background(), name); err == nil {
		t.Error("system admission reopened")
	}
	h.Logon(1)
	if h.ManagerCount() != 0 {
		t.Error("user admission reopened")
	}
	if _, err := m.Status(""); err != nil {
		t.Fatalf("diagnostics unavailable: %v", err)
	}
}

func TestShutdownAllSystemProgressDuringUnknownUserLookup(t *testing.T) {
	const name = "work.service"
	m := managerWith(t, &fakeLauncher{}, map[string]string{name: "[Service]\nExecStart=C:\\Tools\\worker.exe\n"})
	if _, err := m.Start(context.Background(), name); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	proc := m.units[name].proc
	m.mu.Unlock()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	h := NewUserHost(UserHostConfig{QueryToken: func(uint32) (*runtime.UserToken, error) {
		close(entered)
		<-release
		return nil, runtime.ErrNoUserToken
	}})
	go func() { h.Logon(1); close(done) }()
	awaitNativeWork(t, entered)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := ShutdownAll(ctx, m, h); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("shutdown: %v", err)
	}
	if proc.Alive() || h.NativeWorkCount() != 1 {
		t.Error("blocked lookup delayed independent system cleanup or lost ownership")
	}
	unblock()
	awaitNativeWork(t, done)
	if err := ShutdownAll(context.Background(), m, h); err != nil {
		t.Fatal(err)
	}
}

func TestUserHostShutdownIndependentUserAndNativeCleanup(t *testing.T) {
	h, _, procs := testUserHost(t, map[uint32]string{1: testSIDA, 2: testSIDB}, nil)
	h.Logon(1)
	h.Logon(2)
	// A blocked SID gate models accepted creation or another native observer.
	unlock := h.ops.lock(testSIDA)
	var once sync.Once
	release := func() { once.Do(unlock) }
	defer release()
	var closes atomic.Int32
	h.cfg.CloseToken = func(*runtime.UserToken) error {
		if closes.Add(1) == 1 {
			return errors.New("injected close failure")
		}
		return nil
	}
	w, err := h.acceptNativeUserWork()
	if err != nil {
		t.Fatal(err)
	}
	if err := h.finishNativeUserWork(w, &runtime.UserToken{}); err == nil {
		t.Fatal("expected cleanup failure")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := h.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("shutdown: %v", err)
	}
	if procs[testSIDB].Alive() || !procs[testSIDA].Alive() || h.NativeWorkCount() != 0 || closes.Load() != 2 {
		t.Error("blocked user prevented independent cleanup")
	}
	release()
	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.ManagerCount() != 0 {
		t.Fatal("retry did not drain blocked user")
	}
}
