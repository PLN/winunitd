package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/unit"
)

func widePlan(t *testing.T, stop bool) *Transaction {
	t.Helper()
	var units []*unit.Unit
	var names []string
	for i := 0; i < 48; i++ {
		name := fmt.Sprintf("work-%02d.service", i)
		names = append(names, name)
		units = append(units, &unit.Unit{Name: name})
	}
	g, err := Build(units)
	if err != nil {
		t.Fatal(err)
	}
	var tx *Transaction
	if stop {
		tx, err = g.PlanStop(names...)
	} else {
		tx, err = g.PlanStart(names...)
	}
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

func TestLimitedStartCancelsQueuedAndDrainsAdmitted(t *testing.T) {
	tx := widePlan(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var calls atomic.Int32
	done := make(chan *Run, 1)
	go func() {
		run, _ := tx.ExecuteWithLimit(ctx, StartFunc(func(context.Context, string) error {
			if calls.Add(1) == 3 {
				cancel()
				unblock()
			}
			<-release
			return nil
		}), 3)
		done <- run
	}()
	select {
	case run := <-done:
		if calls.Load() != 3 || len(run.Started) != 3 {
			t.Fatalf("admitted %d starts after cancellation, want 3", calls.Load())
		}
		if len(run.Errors) != 45 {
			t.Fatalf("canceled results = %d, want 45", len(run.Errors))
		}
		for _, name := range run.Started {
			if run.StateOf(name) != Active {
				t.Fatal("lost successful admitted completion")
			}
		}
		for _, err := range run.Errors {
			if !errors.Is(err, context.Canceled) {
				t.Fatal("queued start did not report cancellation")
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bounded executor did not finish")
	}
}

func TestLimitedStopContinuesAfterFailures(t *testing.T) {
	tx := widePlan(t, true)
	var active, peak, calls atomic.Int32
	run, err := tx.ExecuteStopWithLimit(context.Background(), StopFunc(func(context.Context, string) error {
		n := active.Add(1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		defer active.Add(-1)
		calls.Add(1)
		return errors.New("injected stop failure")
	}), 1)
	if err == nil || calls.Load() != 48 || len(run.Stopped) != 48 || len(run.Errors) != 48 {
		t.Fatal("stop failure skipped planned cleanup or lost results")
	}
	if peak.Load() != 1 || active.Load() != 0 {
		t.Fatal("stop adapter concurrency exceeded its bound")
	}
}

func TestExecutionRejectsInvalidLimitBeforeSideEffects(t *testing.T) {
	tx := widePlan(t, false)
	called := false
	_, err := tx.ExecuteWithLimit(context.Background(), StartFunc(func(context.Context, string) error { called = true; return nil }), 0)
	if err == nil || called {
		t.Fatal("invalid start limit admitted work")
	}
	tx = widePlan(t, true)
	_, err = tx.ExecuteStopWithLimit(context.Background(), StopFunc(func(context.Context, string) error { called = true; return nil }), -1)
	if err == nil || called {
		t.Fatal("invalid stop limit admitted work")
	}
}
