package timers

import (
	"testing"
	"time"
)

func TestNextDeadlineOnBootVsOnStartup(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	clk := Clock{
		Now:       func() time.Time { return start },
		SinceBoot: func() time.Duration { return time.Hour },
		Startup:   start,
	}

	boot, ok := NextDeadline(Spec{OnBootSec: time.Second, OnBootSecSet: true}, Runtime{}, clk)
	if !ok || !boot.Equal(start) {
		t.Fatalf("OnBootSec already elapsed since machine boot: %v ok=%v", boot, ok)
	}

	startup, ok := NextDeadline(Spec{OnStartupSec: 5 * time.Second, OnStartupSecSet: true}, Runtime{}, clk)
	if !ok || !startup.Equal(start.Add(5*time.Second)) {
		t.Fatalf("OnStartupSec is since this instance: %v ok=%v", startup, ok)
	}

	clkLater := clk
	clkLater.Now = func() time.Time { return start.Add(10 * time.Second) }
	again, ok := NextDeadline(Spec{OnStartupSec: 5 * time.Second, OnStartupSecSet: true}, Runtime{}, clkLater)
	if !ok || !again.Equal(start.Add(10*time.Second)) {
		t.Fatalf("elapsed OnStartupSec fires now: %v ok=%v", again, ok)
	}

	rt := Runtime{FiredBoot: true}
	_, ok = NextDeadline(Spec{OnBootSec: time.Second, OnBootSecSet: true}, rt, clk)
	if ok {
		t.Fatal("OnBootSec must not repeat after it has fired for this activation")
	}
}

func TestNextDeadlinePersistentCatchup(t *testing.T) {
	t.Parallel()
	loc := time.UTC
	now := time.Date(2026, 8, 31, 15, 0, 0, 0, loc)
	clk := Clock{
		Now:       func() time.Time { return now },
		SinceBoot: func() time.Duration { return time.Hour },
		Startup:   now,
	}
	cal, err := ParseCalendar("daily")
	if err != nil {
		t.Fatal(err)
	}
	spec := Spec{OnCalendar: []Calendar{cal}, Persistent: true}

	missed := Runtime{LastActual: now.Add(-48 * time.Hour)}
	got, ok := NextDeadline(spec, missed, clk)
	if !ok || !got.Equal(now) {
		t.Fatalf("Persistent=yes catch-up = %v ok=%v", got, ok)
	}

	fresh, ok := NextDeadline(Spec{OnCalendar: []Calendar{cal}}, Runtime{}, clk)
	if !ok {
		t.Fatal("non-persistent should still schedule the next calendar event")
	}
	want := time.Date(2026, 9, 1, 0, 0, 0, 0, loc)
	if !fresh.Equal(want) {
		t.Fatalf("Persistent=no next = %v, want %v", fresh, want)
	}

	noState, ok := NextDeadline(spec, Runtime{}, clk)
	if !ok || !noState.Equal(want) {
		t.Fatalf("Persistent=yes with no lastActual must not catch up history: %v", noState)
	}
}

func TestNextDeadlineOnUnitActiveSec(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	clk := Clock{Now: func() time.Time { return now }, Startup: now, SinceBoot: func() time.Duration { return time.Hour }}
	_, ok := NextDeadline(Spec{OnUnitActiveSec: time.Second, OnUnitActiveSecSet: true}, Runtime{}, clk)
	if ok {
		t.Fatal("OnUnitActiveSec with no prior activation must not schedule")
	}
	last := now.Add(-500 * time.Millisecond)
	got, ok := NextDeadline(Spec{OnUnitActiveSec: time.Second, OnUnitActiveSecSet: true}, Runtime{LastUnitActive: last}, clk)
	want := last.Add(time.Second)
	if !ok || !got.Equal(want) {
		t.Fatalf("got %v ok=%v, want %v", got, ok, want)
	}

	spec := Spec{OnUnitActiveSec: time.Second, OnUnitActiveSecSet: true}
	fired := Runtime{LastUnitActive: last, LastActual: last.Add(time.Second)}
	if _, ok := NextDeadline(spec, fired, clk); ok {
		t.Fatal("must not re-schedule the same OnUnitActiveSec due after it has fired")
	}
	bumped := fired
	bumped.LastUnitActive = now
	got, ok = NextDeadline(spec, bumped, clk)
	if !ok || !got.Equal(now.Add(time.Second)) {
		t.Fatalf("after UnitActive: got %v ok=%v", got, ok)
	}
}

func TestMarkFiredRelative(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	clk := Clock{
		Now:       func() time.Time { return now },
		SinceBoot: func() time.Duration { return time.Hour },
		Startup:   now.Add(-time.Second),
	}
	spec := Spec{OnBootSecSet: true, OnBootSec: time.Second, OnStartupSecSet: true, OnStartupSec: time.Second}
	rt := Runtime{}
	MarkFired(spec, &rt, clk, now, now)
	if !rt.FiredBoot || !rt.FiredStartup {
		t.Fatalf("fired flags = boot:%v startup:%v", rt.FiredBoot, rt.FiredStartup)
	}
	if !rt.LastActual.Equal(now) {
		t.Fatalf("last actual = %v", rt.LastActual)
	}
}
