package manager

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/runtime"
)

func TestUserIdleCleanupDoesNotBlockSessionListener(t *testing.T) {
	h, _, _ := testUserHost(t, map[uint32]string{1: testSIDA, 2: testSIDB}, nil)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	p := &failingUserMgr{release: release}
	p.alive.Store(true)
	h.mu.Lock()
	h.bySID[testSIDA] = &userInstance{sid: testSIDA, proc: p}
	h.sessions[1] = testSIDA
	h.mu.Unlock()
	ch := make(chan runtime.SessionChange, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { h.Listen(ctx, ch); close(done) }()
	ch <- runtime.SessionChange{SessionID: 1}
	waitCond(t, func() bool { return p.calls.Load() == 1 })
	ch <- runtime.SessionChange{SessionID: 2, Logon: true}
	waitCond(t, func() bool { return h.Alive(testSIDB) })
	cancel()
	awaitNativeWork(t, done)
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer stopCancel()
	if err := h.Shutdown(stopCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked cleanup escaped shutdown: %v", err)
	}
	if p.calls.Load() != 1 {
		t.Fatal("shutdown duplicated pending native stop")
	}
	unblock()
	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestUserIdleCleanupSkipsBusyGatesAndCoalesces(t *testing.T) {
	h := NewUserHost(UserHostConfig{})
	t.Cleanup(h.Close)
	var unlocks []func()
	defer func() {
		for _, unlock := range unlocks {
			unlock()
		}
	}()
	for i := 0; i < 8; i++ {
		sid := fmt.Sprintf("S-1-5-21-1-2-3-%d", 2000+i)
		p := &fakeUserMgr{}
		p.alive.Store(true)
		h.bySID[sid] = &userInstance{sid: sid, proc: p}
		unlocks = append(unlocks, h.ops.lock(sid))
	}
	for sid := range h.bySID {
		for i := 0; i < 100; i++ {
			h.queueIdleCleanup(sid)
		}
	}
	h.mu.Lock()
	if len(h.idleDispatch.pending) != 8 || h.idleDispatch.active != 0 {
		t.Error("busy gates consumed workers or duplicate queue entries")
	}
	p := &fakeUserMgr{}
	p.alive.Store(true)
	h.bySID[testSIDA] = &userInstance{sid: testSIDA, proc: p}
	h.mu.Unlock()
	h.queueIdleCleanup(testSIDA)
	waitCond(t, func() bool { return !p.Alive() })
}

type blockedCleanupLiveness struct {
	fakeUserMgr
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	killed  atomic.Bool
}

func (p *blockedCleanupLiveness) Kill() error { p.killed.Store(true); return nil }
func (p *blockedCleanupLiveness) Alive() bool {
	if p.killed.Load() {
		p.once.Do(func() { close(p.entered) })
		<-p.release
	}
	return !p.killed.Load()
}

func TestUserCleanupDeadlineOwnsLivenessProbe(t *testing.T) {
	h := NewUserHost(UserHostConfig{})
	p := &blockedCleanupLiveness{entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	unblock := func() { once.Do(func() { close(p.release) }) }
	defer unblock()
	t.Cleanup(h.Close)
	h.bySID[testSIDA] = &userInstance{sid: testSIDA, proc: p}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := h.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked liveness escaped deadline: %v", err)
	}
	awaitNativeWork(t, p.entered)
	if h.ManagerCount() != 1 {
		t.Fatal("pending probe lost ownership")
	}
	unblock()
	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestUserIdleCleanupBoundsWorkers(t *testing.T) {
	h := NewUserHost(UserHostConfig{})
	t.Cleanup(h.Close)
	release := make(chan struct{})
	defer close(release)
	var procs []*failingUserMgr
	var sids []string
	for i := 0; i < 12; i++ {
		sid := fmt.Sprintf("S-1-5-21-1-2-3-%d", 3000+i)
		p := &failingUserMgr{release: release}
		p.alive.Store(true)
		procs = append(procs, p)
		sids = append(sids, sid)
		h.bySID[sid] = &userInstance{sid: sid, proc: p}
	}
	for _, sid := range sids {
		h.queueIdleCleanup(sid)
	}
	waitCond(t, func() bool {
		var calls int32
		for _, p := range procs {
			calls += p.calls.Load()
		}
		return calls == userIdleCleanupWorkers
	})
	for i := 0; i < 100; i++ {
		for _, sid := range sids {
			h.queueIdleCleanup(sid)
		}
		h.queueIdleCleanup("unknown")
	}
	h.mu.Lock()
	if h.idleDispatch.active != userIdleCleanupWorkers || len(h.idleDispatch.pending) != len(sids) {
		t.Error("cleanup dispatch exceeded its worker or coalescing bound")
	}
	h.mu.Unlock()
}

func TestUserIdleCleanupFinishesUncertainStopAfterNewLogon(t *testing.T) {
	h := NewUserHost(UserHostConfig{})
	t.Cleanup(h.Close)
	p := &fakeUserMgr{}
	p.alive.Store(true)
	h.bySID[testSIDA] = &userInstance{sid: testSIDA, proc: p, uncertain: true}
	h.sessions[2] = testSIDA
	h.queueIdleCleanup(testSIDA)
	waitCond(t, func() bool { return h.ManagerCount() == 0 })
	if p.Alive() {
		t.Fatal("new logon abandoned already accepted cleanup")
	}
}
