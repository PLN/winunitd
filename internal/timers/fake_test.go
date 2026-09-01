package timers

import (
	"runtime"
	"testing"
	"time"
)

func TestFakeAdvanceFiresTimer(t *testing.T) {
	t.Parallel()
	fk := NewFake(time.Time{})
	tm := fk.Clock().Timer(5 * time.Second)
	select {
	case <-tm.C():
		t.Fatal("fired before Advance")
	default:
	}
	fk.Advance(5 * time.Second)
	select {
	case <-tm.C():
	default:
		t.Fatal("Advance did not fire the timer")
	}
}

func TestFakeAdvanceDoesNotFireEarly(t *testing.T) {
	t.Parallel()
	fk := NewFake(time.Time{})
	tm := fk.Clock().Timer(time.Second)
	fk.Advance(time.Second - time.Nanosecond)
	select {
	case <-tm.C():
		t.Fatal("fired before deadline")
	default:
	}
	fk.Advance(time.Nanosecond)
	select {
	case <-tm.C():
	default:
		t.Fatal("exact deadline must fire")
	}
}

func TestFakeJumpWallDoesNotFireMonotonicTimer(t *testing.T) {
	t.Parallel()
	fk := NewFake(time.Time{})
	tm := fk.Clock().Timer(time.Hour)
	fk.JumpWall(fk.Now().Add(2 * time.Hour))
	select {
	case <-tm.C():
		t.Fatal("JumpWall must not fire NewTimer waits")
	default:
	}
	select {
	case <-fk.Clock().Changed:
	default:
		t.Fatal("JumpWall must signal Changed")
	}
}

func TestFakeSuspendLeavesSinceBoot(t *testing.T) {
	t.Parallel()
	fk := NewFake(time.Time{})
	boot := fk.SinceBoot()
	wall := fk.Now()
	fk.Suspend(2 * time.Hour)
	if fk.SinceBoot() != boot {
		t.Fatalf("SinceBoot = %s, want %s", fk.SinceBoot(), boot)
	}
	if !fk.Now().Equal(wall.Add(2 * time.Hour)) {
		t.Fatalf("wall = %v", fk.Now())
	}
}

func waitFired(t *testing.T, ch <-chan string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case name := <-ch:
			return name
		default:
			runtime.Gosched()
		}
	}
	t.Fatal("timer did not fire")
	return ""
}

func waitQuiet(t *testing.T, ch <-chan string) {
	t.Helper()
	for i := 0; i < 20_000; i++ {
		select {
		case name := <-ch:
			t.Fatalf("unexpected fire %q", name)
		default:
			runtime.Gosched()
		}
	}
}

func waitNext(t *testing.T, e *Engine, name string, want time.Time) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got := e.Status(name).Next
		if got.Equal(want) {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("next = %v, want %v", e.Status(name).Next, want)
}
