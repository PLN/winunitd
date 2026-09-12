package timers

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func waitStorageReady(t *testing.T, e *Engine, name string) {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		s := e.Status(name)
		if s.StorageState != "loading" {
			if s.StorageError != "" {
				t.Fatal(s.StorageError)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timer storage did not finish loading")
}

func TestTimerBlockedLoadDoesNotHoldDecisionLock(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var loads atomic.Int32
	store := &Store{load: func(string) (Runtime, error) {
		if loads.Add(1) == 1 {
			close(entered)
		}
		<-release
		return Runtime{}, nil
	}}
	e := NewEngine(NewFake(time.Time{}).Clock(), store, nil)
	defer func() { unblock(); e.Stop() }()
	first := e.Arm(Spec{Name: "work.timer", OnStartupSecSet: true})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("load not dispatched")
	}
	decision := make(chan struct{})
	go func() {
		e.Disarm("work.timer")
		e.Arm(Spec{Name: "work.timer", OnStartupSec: time.Hour, OnStartupSecSet: true})
		if e.Current("work.timer", first) {
			t.Error("old arm remains current")
		}
		if e.Status("work.timer").StorageState != "loading" {
			t.Error("pending load not visible")
		}
		close(decision)
	}()
	select {
	case <-decision:
	case <-time.After(time.Second):
		t.Fatal("storage holds decision lock")
	}
	stopped := make(chan struct{})
	go func() { e.Stop(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("stop lost pending I/O")
	case <-time.After(30 * time.Millisecond):
	}
	// Every Stop caller must join the same accepted native work.
	second := make(chan struct{})
	go func() { e.Stop(); close(second) }()
	select {
	case <-second:
		t.Fatal("repeated stop escaped pending I/O")
	case <-time.After(30 * time.Millisecond):
	}
	unblock()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("stop did not drain")
	}
	select {
	case <-second:
	case <-time.After(time.Second):
		t.Fatal("second stop did not drain")
	}
	if loads.Load() != 1 {
		t.Fatal("shutdown dispatched replacement load")
	}
}

func TestTimerWriteFailureSuspendsActivationAndCanBeRepaired(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	store := &Store{save: func(string, Runtime) error {
		if fail.Load() {
			return errors.New("storage unavailable")
		}
		return nil
	}}
	clock := NewFake(time.Time{})
	fired := make(chan string, 4)
	e := NewEngine(clock.Clock(), store, func(f Fire) { fired <- f.Name })
	defer e.Stop()
	spec := Spec{Name: "work.timer", OnStartupSec: time.Second, OnStartupSecSet: true}
	e.Arm(spec)
	clock.Advance(2 * time.Second)
	until := time.Now().Add(time.Second)
	for e.Status(spec.Name).StorageState != "failed" && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	s := e.Status(spec.Name)
	if s.StorageState != "failed" || s.StorageError == "" || !s.Next.IsZero() {
		t.Fatalf("failure concealed: %+v", s)
	}
	select {
	case <-fired:
		t.Fatal("activation preceded durable write")
	default:
	}
	fail.Store(false)
	e.Arm(spec)
	waitStorageReady(t, e, spec.Name)
	waitFired(t, fired)
}

func TestTimerStaleBlockedWriteCannotActivateReplacement(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var saves atomic.Int32
	store := &Store{save: func(string, Runtime) error {
		if saves.Add(1) == 1 {
			close(entered)
			<-release
		}
		return nil
	}}
	clock := NewFake(time.Time{})
	fired := make(chan string, 4)
	e := NewEngine(clock.Clock(), store, func(f Fire) { fired <- f.Unit })
	defer func() { unblock(); e.Stop() }()
	spec := Spec{Name: "work.timer", Unit: "old.service", OnStartupSec: time.Second, OnStartupSecSet: true}
	e.Arm(spec)
	clock.Advance(2 * time.Second)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("write did not enter")
	}
	e.Disarm(spec.Name)
	spec.Unit = "new.service"
	spec.OnStartupSec = time.Hour
	e.Arm(spec)
	if e.Status(spec.Name).Next.IsZero() {
		t.Fatal("replacement decision blocked by old write")
	}
	unblock()
	e.fires.Wait()
	select {
	case got := <-fired:
		t.Fatalf("stale write dispatched %s", got)
	default:
	}
}

func TestTimerStoreRejectsCorruptionAndPreservesReplacement(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"{", "null", `{"version":3}`, `{"lastActual":"invalid"}`, strings.Repeat("x", 16385)} {
		if err := os.WriteFile(filepath.Join(store.dir, "work.timer.json"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LoadChecked("work.timer"); err == nil {
			t.Fatalf("accepted corrupt state %q", body[:min(40, len(body))])
		}
	}
	stamp := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	want := Runtime{LastActual: stamp, LastSuccess: stamp, LastScheduled: stamp}
	if err := store.Save("work.timer", want); err != nil {
		t.Fatal(err)
	}
	if got, err := store.LoadChecked("work.timer"); err != nil || got != want {
		t.Fatalf("replacement %+v %v", got, err)
	}
}

func TestTimerArmBudget(t *testing.T) {
	e, _ := testEngine(t, nil)
	for i := 0; i < MaxArmedTimers; i++ {
		if e.Arm(Spec{Name: fmt.Sprintf("work-%d.timer", i)}) == 0 {
			t.Fatal("early rejection")
		}
	}
	if e.Arm(Spec{Name: "overflow.timer"}) != 0 {
		t.Fatal("arm limit bypassed")
	}
	e.Disarm("work-0.timer")
	if e.Arm(Spec{Name: "overflow.timer"}) == 0 {
		t.Fatal("disarm did not restore capacity")
	}
}
