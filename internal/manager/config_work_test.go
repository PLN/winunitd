package manager

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/unit"
)

func TestShutdownAllJoinsReloadAndRejectsLatePublication(t *testing.T) {
	m := testManager(t, map[string]string{"work.service": "[Service]\nExecStart=C:\\Tools\\work.exe\n"})
	m.mu.Lock()
	revision := m.configRevision
	m.mu.Unlock()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	done := make(chan error, 1)
	go func() {
		_, err := m.reloadWithBuilder(func(units []*unit.Unit) (*core.Graph, error) { close(entered); <-release; return core.Build(units) })
		done <- err
	}()
	awaitNativeWork(t, entered)
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		err := ShutdownAll(ctx, m, nil)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("shutdown escaped accepted reload: %v", err)
		}
	}
	unblock()
	if err := waitErr(t, done); err == nil {
		t.Error("late reload reported successful publication")
	}
	if err := ShutdownAll(context.Background(), m, nil); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	got, work := m.configRevision, m.configWork
	m.mu.Unlock()
	if got != revision || work != nil {
		t.Fatal("late reload changed configuration or retained completed work")
	}
	if _, err := m.Reload(); err == nil {
		t.Error("closed manager admitted reload")
	}
	if _, err := m.Enable("work"); err == nil {
		t.Error("closed manager admitted enable")
	}
	if _, err := m.Disable("work"); err == nil {
		t.Error("closed manager admitted disable")
	}
}

func TestUserHostShutdownJoinsLingerAccountMutation(t *testing.T) {
	for _, enable := range []bool{true, false} {
		t.Run(map[bool]string{true: "enable", false: "disable"}[enable], func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			var starts atomic.Int32
			h := NewUserHost(UserHostConfig{
				LingerDir: t.TempDir(),
				Lookup: func(string) (runtime.UserInfo, error) {
					close(entered)
					<-release
					return runtime.UserInfo{SID: testSIDA, Username: "alice"}, nil
				},
				LingerToken: func(runtime.LingerRecord) (*runtime.UserToken, error) {
					starts.Add(1)
					return nil, runtime.ErrNoLingerToken
				},
			})
			if !enable {
				if err := h.store.Put(runtime.LingerRecord{SID: testSIDA, Name: "alice"}); err != nil {
					t.Fatal(err)
				}
			}
			done := make(chan error, 1)
			go func() {
				var err error
				if enable {
					_, err = h.EnableLinger("alice")
				} else {
					_, err = h.DisableLinger("alice")
				}
				done <- err
			}()
			awaitNativeWork(t, entered)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			if err := h.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("shutdown escaped account mutation: %v", err)
			}
			if h.NativeWorkCount() != 1 {
				t.Error("account mutation not retained")
			}
			unblock()
			if err := waitErr(t, done); err != nil {
				t.Fatal(err)
			}
			if err := h.Shutdown(context.Background()); err != nil {
				t.Fatal(err)
			}
			if h.store.Has(testSIDA) != enable || starts.Load() != 0 || h.NativeWorkCount() != 0 {
				t.Error("accepted mutation not drained or late launch occurred")
			}
			if _, err := h.EnableLinger("alice"); err == nil {
				t.Error("closed host admitted linger enable")
			}
			if _, err := h.DisableLinger("alice"); err == nil {
				t.Error("closed host admitted linger disable")
			}
		})
	}
}
