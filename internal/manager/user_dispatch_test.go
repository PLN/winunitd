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

func TestUserListenerProcessesLogoffWhileTokenLookupBlocked(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var startsA, startsB atomic.Int32
	h := NewUserHost(UserHostConfig{
		Admission:      UserAdmission{Mode: "unit-files"},
		ProbeUserUnits: func(*runtime.UserToken) (bool, error) { return true, nil },
		QueryToken: func(id uint32) (*runtime.UserToken, error) {
			sid := testSIDB
			if id == 1 {
				close(entered)
				<-release
				sid = testSIDA
			}
			return &runtime.UserToken{Info: runtime.UserInfo{SID: sid}}, nil
		},
		Start: func(spec runtime.UserManagerSpec) (runtime.UserManagerProc, error) {
			if spec.SID == testSIDA {
				startsA.Add(1)
			} else {
				startsB.Add(1)
			}
			p := &fakeUserMgr{sid: spec.SID}
			p.alive.Store(true)
			return p, nil
		},
	})
	t.Cleanup(h.Close)
	events := make(chan runtime.SessionChange, 3)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listenDone := make(chan struct{})
	go func() { h.Listen(ctx, events); close(listenDone) }()
	events <- runtime.SessionChange{SessionID: 1, Logon: true}
	awaitNativeWork(t, entered)
	events <- runtime.SessionChange{SessionID: 1, Logon: false}
	events <- runtime.SessionChange{SessionID: 2, Logon: true}
	waitCond(t, func() bool { return startsB.Load() == 1 })
	unblock()
	waitCond(t, func() bool { return h.NativeWorkCount() == 0 })
	if startsA.Load() != 0 {
		t.Fatal("late token resurrected logged-off session")
	}
	cancel()
	awaitNativeWork(t, listenDone)
}

func TestUserListenerBoundsAcceptedLogonWorkers(t *testing.T) {
	entered := make(chan struct{}, maxNativeUserWork)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var queries, closes atomic.Int32
	h := NewUserHost(UserHostConfig{
		QueryToken: func(uint32) (*runtime.UserToken, error) {
			queries.Add(1)
			entered <- struct{}{}
			<-release
			return &runtime.UserToken{}, runtime.ErrNoUserToken
		},
		CloseToken: func(*runtime.UserToken) error { closes.Add(1); return nil },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan runtime.SessionChange)
	listenDone := make(chan struct{})
	go func() { h.Listen(ctx, events); close(listenDone) }()
	for i := 0; i < maxNativeUserWork; i++ {
		events <- runtime.SessionChange{SessionID: uint32(i + 1), Logon: true}
		awaitNativeWork(t, entered)
	}
	sent := make(chan struct{})
	go func() {
		for i := uint32(10); i < 110; i++ {
			events <- runtime.SessionChange{SessionID: i, Logon: true}
		}
		close(sent)
	}()
	awaitNativeWork(t, sent)
	if queries.Load() != maxNativeUserWork || h.NativeWorkCount() != maxNativeUserWork {
		t.Fatal("notification flood exceeded native work limit")
	}
	cancel()
	awaitNativeWork(t, listenDone)
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer stopCancel()
	if err := h.Shutdown(stopCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("pending workers escaped shutdown: %v", err)
	}
	unblock()
	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if closes.Load() != maxNativeUserWork || h.NativeWorkCount() != 0 {
		t.Fatal("accepted workers did not drain")
	}
}
