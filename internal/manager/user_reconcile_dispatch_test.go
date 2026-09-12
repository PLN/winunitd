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

func TestUserReconcileDispatchPassesBlockedFirstLookup(t *testing.T) {
	release, entered := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var last atomic.Bool
	h := NewUserHost(UserHostConfig{
		Sessions: func() ([]uint32, error) { return []uint32{1, 2, 3, 4, 5, 6, 7, 8}, nil },
		QueryToken: func(id uint32) (*runtime.UserToken, error) {
			if id == 1 {
				close(entered)
				<-release
			}
			if id == 8 {
				last.Store(true)
			}
			return nil, runtime.ErrNoUserToken
		},
	})
	t.Cleanup(h.Close)
	h.scheduleReconcile()
	awaitNativeWork(t, entered)
	waitCond(t, last.Load)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := h.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("reconciliation lost its blocked worker: %v", err)
	}
	unblock()
	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestUserReconcileDispatchSkipsBusyUserLaunch(t *testing.T) {
	release, entered := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var firstStarts, otherStarts atomic.Int32
	h := NewUserHost(UserHostConfig{
		Admission:      UserAdmission{Mode: "unit-files"},
		ProbeUserUnits: func(*runtime.UserToken) (bool, error) { return true, nil },
		Sessions:       func() ([]uint32, error) { return []uint32{1, 2, 3, 4, 5, 6, 7, 8}, nil },
		QueryToken: func(id uint32) (*runtime.UserToken, error) {
			if id != 1 {
				<-entered
			}
			sid := testSIDA
			if id == 8 {
				sid = testSIDB
			}
			return &runtime.UserToken{Info: runtime.UserInfo{SID: sid}}, nil
		},
		Start: func(spec runtime.UserManagerSpec) (runtime.UserManagerProc, error) {
			if spec.SID == testSIDA {
				if firstStarts.Add(1) == 1 {
					close(entered)
				}
				<-release
			} else {
				otherStarts.Add(1)
			}
			p := &fakeUserMgr{}
			p.alive.Store(true)
			return p, nil
		},
	})
	t.Cleanup(h.Close)
	h.scheduleReconcile()
	awaitNativeWork(t, entered)
	waitCond(t, func() bool { return otherStarts.Load() == 1 })
	if firstStarts.Load() != 1 {
		t.Fatal("same-user sessions created duplicate launches")
	}
	unblock()
	waitCond(t, func() bool { return h.NativeWorkCount() == 0 })
	if h.ManagerCount() != 2 {
		t.Fatal("independent user did not retain its manager")
	}
}
