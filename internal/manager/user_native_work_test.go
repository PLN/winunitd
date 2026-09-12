package manager

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/runtime"
)

func awaitNativeWork(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("native work did not reach expected boundary")
	}
}

func TestUserHostShutdownTracksUnknownSIDLookup(t *testing.T) {
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var closes, starts atomic.Int32
	h := NewUserHost(UserHostConfig{
		Admission:      UserAdmission{Mode: "unit-files"},
		ProbeUserUnits: func(*runtime.UserToken) (bool, error) { return true, nil },
		QueryToken: func(uint32) (*runtime.UserToken, error) {
			close(entered)
			<-release
			return &runtime.UserToken{Info: runtime.UserInfo{SID: testSIDA}}, nil
		},
		CloseToken: func(*runtime.UserToken) error { closes.Add(1); return nil },
		Start: func(runtime.UserManagerSpec) (runtime.UserManagerProc, error) {
			starts.Add(1)
			return nil, errors.New("unexpected launch")
		},
	})
	go func() { h.Logon(1); close(done) }()
	awaitNativeWork(t, entered)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := h.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("shutdown: %v", err)
	}
	if h.NativeWorkCount() != 1 || h.ManagerCount() != 0 {
		t.Error("unknown-SID work was not retained")
	}
	close(release)
	awaitNativeWork(t, done)
	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if closes.Load() != 1 || starts.Load() != 0 || h.NativeWorkCount() != 0 {
		t.Fatalf("closes=%d starts=%d work=%d", closes.Load(), starts.Load(), h.NativeWorkCount())
	}
}

func TestUserHostShutdownTracksEnumeration(t *testing.T) {
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var queries atomic.Int32
	h := NewUserHost(UserHostConfig{
		Sessions:   func() ([]uint32, error) { close(entered); <-release; return []uint32{1}, nil },
		QueryToken: func(uint32) (*runtime.UserToken, error) { queries.Add(1); return nil, errors.New("unexpected lookup") },
	})
	go func() { h.Reconcile(); close(done) }()
	awaitNativeWork(t, entered)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := h.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("shutdown: %v", err)
	}
	if h.NativeWorkCount() != 1 {
		t.Error("enumeration lost")
	}
	close(release)
	awaitNativeWork(t, done)
	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if queries.Load() != 0 || h.NativeWorkCount() != 0 {
		t.Error("late enumeration resurrected work")
	}
}

func TestUserHostNativeAdmissionIncludesFailedTokenCleanup(t *testing.T) {
	var queries, closes atomic.Int32
	var fail atomic.Bool
	fail.Store(true)
	h := NewUserHost(UserHostConfig{
		QueryToken: func(uint32) (*runtime.UserToken, error) {
			queries.Add(1)
			return &runtime.UserToken{}, errors.New("lookup failed with cleanup obligation")
		},
		CloseToken: func(*runtime.UserToken) error {
			closes.Add(1)
			if fail.Load() {
				return errors.New("close failed")
			}
			return nil
		},
		Sessions: func() ([]uint32, error) { return nil, nil },
	})
	for i := 0; i < maxNativeUserWork+3; i++ {
		h.Logon(uint32(i + 1))
	}
	if queries.Load() != maxNativeUserWork || h.NativeWorkCount() != maxNativeUserWork {
		t.Fatalf("limit escaped: queries=%d work=%d", queries.Load(), h.NativeWorkCount())
	}
	fail.Store(false)
	h.Reconcile()
	if h.NativeWorkCount() != 0 || closes.Load() != 2*maxNativeUserWork {
		t.Fatalf("reconciliation did not retry cleanup: work=%d closes=%d", h.NativeWorkCount(), closes.Load())
	}
	h.Logon(20)
	if queries.Load() != maxNativeUserWork+1 || h.NativeWorkCount() != 0 {
		t.Error("admission did not reopen after cleanup")
	}
}

func TestUserHostShutdownJoinsBlockedTokenClose(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var closes atomic.Int32
	h := NewUserHost(UserHostConfig{
		QueryToken: func(uint32) (*runtime.UserToken, error) { return &runtime.UserToken{}, errors.New("lookup failed") },
		CloseToken: func(*runtime.UserToken) error {
			n := closes.Add(1)
			if n == 1 {
				return errors.New("initial close failed")
			}
			if n == 2 {
				close(entered)
			}
			<-release
			return nil
		},
	})
	h.Logon(1)
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		err := h.Shutdown(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("shutdown retry %d: %v", i, err)
		}
	}
	awaitNativeWork(t, entered)
	if closes.Load() != 2 || h.NativeWorkCount() != 1 {
		t.Error("retry duplicated blocked close or discarded ownership")
	}
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := h.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if closes.Load() != 2 || h.NativeWorkCount() != 0 {
		t.Error("cleanup did not finish once")
	}
}

func TestUserHostLingerErrorRetainsTokenCleanup(t *testing.T) {
	lookupErr, closeErr := errors.New("linger lookup failed"), errors.New("close failed")
	var closes atomic.Int32
	h := NewUserHost(UserHostConfig{
		LingerToken: func(runtime.LingerRecord) (*runtime.UserToken, error) { return &runtime.UserToken{}, lookupErr },
		CloseToken: func(*runtime.UserToken) error {
			if closes.Add(1) == 1 {
				return closeErr
			}
			return nil
		},
	})
	err := h.startLinger(runtime.LingerRecord{SID: testSIDA})
	if !errors.Is(err, lookupErr) || !errors.Is(err, closeErr) || h.NativeWorkCount() != 1 {
		t.Fatalf("cleanup obligation lost: %v", err)
	}
	if err := h.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if closes.Load() != 2 || h.NativeWorkCount() != 0 {
		t.Error("linger cleanup not retried")
	}
}
