package core

import (
	"testing"
	"time"
)

func TestStartLimitHit(t *testing.T) {
	t.Parallel()
	interval := 10 * time.Second
	burst := 5
	t0 := time.Time{}

	if StartLimitHit(nil, t0, interval, burst) {
		t.Fatal("empty history must not hit")
	}

	var starts []time.Time
	for i := 0; i < burst; i++ {
		now := t0.Add(time.Duration(i) * time.Second)
		starts = RecordStart(starts, now, interval, burst)
		hit := StartLimitHit(starts, now, interval, burst)
		if i < burst-1 && hit {
			t.Fatalf("hit after %d starts", i+1)
		}
		if i == burst-1 && !hit {
			t.Fatal("burst starts inside interval must hit")
		}
	}
}

func TestStartLimitBurstZeroNeverHits(t *testing.T) {
	t.Parallel()
	interval := time.Second
	var starts []time.Time
	now := time.Time{}
	for i := 0; i < 20; i++ {
		starts = RecordStart(starts, now, interval, 0)
		if StartLimitHit(starts, now, interval, 0) {
			t.Fatal("StartLimitBurst=0 is unlimited")
		}
	}
	if starts != nil {
		t.Fatal("disabled limit must not retain timestamps")
	}
}

func TestStartLimitIntervalZeroNeverHits(t *testing.T) {
	t.Parallel()
	now := time.Time{}
	starts := RecordStart(nil, now, 0, 5)
	if StartLimitHit(starts, now, 0, 5) {
		t.Fatal("StartLimitIntervalSec=0 is unlimited")
	}
}

func TestStartLimitWindowSlides(t *testing.T) {
	t.Parallel()
	interval := 5 * time.Second
	burst := 2
	t0 := time.Time{}
	starts := RecordStart(nil, t0, interval, burst)
	later := t0.Add(6 * time.Second)
	starts = RecordStart(starts, later, interval, burst)
	if StartLimitHit(starts, later, interval, burst) {
		t.Fatal("start outside the interval must drop out of the window")
	}
	if len(starts) != 1 {
		t.Fatalf("pruned starts = %d, want 1", len(starts))
	}
}
