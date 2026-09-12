package manager

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/runtime"
)

func TestUserReconcileHasCapacityDuringAdmissionSaturation(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	entered := make(chan struct{}, maxNativeUserWork)
	var enumerations atomic.Int32
	h := NewUserHost(UserHostConfig{
		QueryToken: func(uint32) (*runtime.UserToken, error) {
			entered <- struct{}{}
			<-release
			return nil, runtime.ErrNoUserToken
		},
		Sessions: func() ([]uint32, error) { enumerations.Add(1); return nil, nil },
	})
	t.Cleanup(h.Close)
	p := &fakeUserMgr{}
	p.alive.Store(true)
	h.bySID[testSIDA] = &userInstance{sid: testSIDA, proc: p}
	h.sessions[100] = testSIDA
	for i := 0; i < maxNativeUserWork; i++ {
		h.dispatchLogon(uint32(i+1), true)
		awaitNativeWork(t, entered)
	}
	done := make(chan struct{})
	go func() { h.Reconcile(); close(done) }()
	awaitNativeWork(t, done)
	if enumerations.Load() != 1 || h.ManagerCount() != 0 || p.Alive() {
		t.Fatal("admission saturation prevented missed-logoff cleanup")
	}
	unblock()
	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestUserPolicyProgressDuringBlockedReconcile(t *testing.T) {
	release, entered := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var calls atomic.Int32
	h := NewUserHost(UserHostConfig{Admission: UserAdmission{Mode: "unit-files"}, Sessions: func() ([]uint32, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-release
		return nil, nil
	}})
	t.Cleanup(h.Close)
	h.scheduleReconcile()
	awaitNativeWork(t, entered)
	for i := 0; i < 100; i++ {
		h.scheduleReconcile()
	}
	path := filepath.Join(t.TempDir(), "admission.json")
	h.scheduleAdmissionRefresh(path)
	waitCond(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.admission.Mode == "explicit"
	})
	if calls.Load() != 1 {
		t.Fatal("reconciliation flood spawned another native enumeration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := h.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("reserved worker escaped shutdown: %v", err)
	}
	unblock()
	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
