package manager

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func opEntries(o *unitOps) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.by)
}

func TestUnitOpsReclaimsHistoricalNames(t *testing.T) {
	var o unitOps
	for i := 0; i < 2000; i++ {
		name := fmt.Sprintf("worker-%d.service", i)
		unlock := o.lock(name)
		if other, ok := o.tryLock(name); ok {
			other()
			t.Fatal("tryLock bypassed the current holder")
		}
		unlock()
		if n := opEntries(&o); n != 0 {
			t.Fatalf("completed operation retained %d historical names", n)
		}
	}
}

func TestUnitOpsCanceledWaiterPreservesHolder(t *testing.T) {
	var o unitOps
	unlock := o.lock("worker.service")
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &observedDoneContext{Context: base, observed: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		u, err := o.lockContext(ctx, "worker.service")
		if u != nil {
			u()
		}
		done <- err
	}()
	select {
	case <-ctx.observed:
	case <-time.After(5 * time.Second):
		t.Fatal("waiter did not enter cancellation select")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter: %v", err)
	}
	if u, ok := o.tryLock("worker.service"); ok {
		u()
		t.Fatal("cancellation split the holder's gate")
	}
	u, ok := o.tryLock("independent.service")
	if !ok {
		t.Fatal("one unit blocked an independent unit")
	}
	u()
	unlock()
	if n := opEntries(&o); n != 0 {
		t.Fatalf("canceled/finished operations retained %d entries", n)
	}
}

// Done is evaluated inside the acquisition select, after the gate is pinned.
type observedDoneContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (c *observedDoneContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}

func TestUnitOpsReclamationNeverSplitsContendedGate(t *testing.T) {
	var o unitOps
	var active, overlaps atomic.Int32
	var wg sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				var unlock func()
				if i%2 == 0 {
					var ok bool
					unlock, ok = o.tryLock("shared.service")
					if !ok {
						runtime.Gosched()
						continue
					}
				} else {
					unlock = o.lock("shared.service")
				}
				if active.Add(1) != 1 {
					overlaps.Add(1)
				}
				runtime.Gosched()
				active.Add(-1)
				unlock()
			}
		}()
	}
	wg.Wait()
	if overlaps.Load() != 0 || active.Load() != 0 {
		t.Fatal("reclamation allowed concurrent owners of the same unit")
	}
	if n := opEntries(&o); n != 0 {
		t.Fatalf("drained contention retained %d entries", n)
	}
}
