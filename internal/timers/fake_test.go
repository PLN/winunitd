package timers

import (
	"math"
	"runtime"
	"testing"
	"time"
)

func TestFakeLongWaitKeepsRemainingBudget(t *testing.T) {
	fk := NewFake(time.Time{})
	tm := fk.Clock().Timer(time.Duration(math.MaxInt64))
	fk.Advance(time.Second)
	if !fk.WaitingAt(time.Duration(math.MaxInt64) - time.Second) {
		t.Fatal("long wait overflowed its uptime deadline")
	}
	select {
	case <-tm.C():
		t.Fatal("long wait fired early")
	default:
	}
}

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

func TestFakeWaitingAtSeesLaterDeadline(t *testing.T) {
	t.Parallel()
	fk := NewFake(time.Time{})
	_ = fk.Clock().Timer(time.Second)
	if fk.WaitingAt(2 * time.Second) {
		t.Fatal("2s wait is not armed")
	}
	_ = fk.Clock().Timer(2 * time.Second)
	if !fk.WaitingAt(2 * time.Second) {
		t.Fatal("2s wait must be visible while a 1s wait is also pending")
	}
	when, ok := fk.NextWhen()
	if !ok || !when.Equal(fk.Now().Add(time.Second)) {
		t.Fatalf("NextWhen = %v ok=%v, want the 1s wait", when, ok)
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
	fk.Advance(30 * time.Minute)
	select {
	case <-tm.C():
		t.Fatal("advance after a wall jump fired a partly elapsed wait")
	default:
	}
	fk.JumpWall(fk.Now().Add(-3 * time.Hour))
	if when, ok := fk.NextWhen(); !ok || !when.Equal(fk.Now().Add(30*time.Minute)) {
		t.Fatalf("remaining monotonic wait projection = %v, %v", when, ok)
	}
	fk.Advance(30 * time.Minute)
	select {
	case <-tm.C():
	default:
		t.Fatal("backward wall jump delayed an elapsed wait")
	}
}

func TestFakeSuspendIncludesWindowsUptime(t *testing.T) {
	t.Parallel()
	fk := NewFake(time.Time{})
	boot := fk.SinceBoot()
	wall := fk.Now()
	tm := fk.Clock().Timer(time.Hour)
	fk.Suspend(2 * time.Hour)
	if fk.SinceBoot() != boot+2*time.Hour || fk.SinceStart() != 2*time.Hour {
		t.Fatalf("resume uptime = %s/%s", fk.SinceBoot(), fk.SinceStart())
	}
	if !fk.Now().Equal(wall.Add(2 * time.Hour)) {
		t.Fatalf("wall = %v", fk.Now())
	}
	select {
	case <-tm.C():
	default:
		t.Fatal("resume did not deliver the elapsed wait")
	}
	select {
	case <-fk.Clock().Changed:
	default:
		t.Fatal("resume did not signal clock reconciliation")
	}
}

func TestFakeSinceStartIgnoresJumpWall(t *testing.T) {
	t.Parallel()
	fk := NewFake(time.Time{})
	if fk.SinceStart() != 0 {
		t.Fatalf("SinceStart = %s, want 0", fk.SinceStart())
	}
	fk.Advance(5 * time.Second)
	if fk.SinceStart() != 5*time.Second {
		t.Fatalf("SinceStart after Advance = %s", fk.SinceStart())
	}
	fk.JumpWall(fk.Now().Add(time.Hour))
	if fk.SinceStart() != 5*time.Second {
		t.Fatalf("JumpWall moved SinceStart: %s", fk.SinceStart())
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
