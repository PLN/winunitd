package timers

import (
	"sync/atomic"
	"testing"
	"time"
	_ "time/tzdata" // Qualification binaries must not depend on an installed Go tree.
)

func TestEngineResumeReconcilesAllClockDomains(t *testing.T) {
	fk := NewFake(time.Time{})
	clk := fk.Clock()
	clk.Changed = nil
	got := make(chan string, 8)
	e := NewEngine(clk, nil, func(f Fire) { got <- f.Name })
	defer e.Stop()
	specs := []Spec{
		{Name: "boot.timer", OnBootSec: 2 * time.Hour, OnBootSecSet: true},
		{Name: "startup.timer", OnStartupSec: time.Hour, OnStartupSecSet: true},
		{Name: "active.timer", Unit: "work.service", OnUnitActiveSec: time.Hour, OnUnitActiveSecSet: true},
		{Name: "calendar.timer", OnCalendar: []Calendar{mustCal(t, "*-*-* 13:00:00")}},
	}
	for _, spec := range specs {
		e.Arm(spec)
	}
	e.UnitActive("work.service", fk.Now())
	for _, spec := range specs {
		waitNext(t, e, spec.Name, fk.Now().Add(time.Hour))
	}
	fk.Suspend(2 * time.Hour)
	e.ClockChanged()
	e.fires.Wait()
	seen := make(map[string]bool)
	for range specs {
		select {
		case name := <-got:
			if seen[name] {
				t.Fatalf("duplicate resume activation for %s", name)
			}
			seen[name] = true
		default:
			t.Fatalf("resume delivered only %v", seen)
		}
	}
	for _, spec := range specs {
		if !seen[spec.Name] {
			t.Fatalf("resume lost %s", spec.Name)
		}
	}
	e.ClockChanged()
	e.fires.Wait()
	select {
	case name := <-got:
		t.Fatalf("repeated resume delivered %s again", name)
	default:
	}
}

func TestEngineCalendarDeliversDSTGapAndFoldOnce(t *testing.T) {
	cases := []struct {
		name, zone, expression string
		month                  time.Month
		day, firstUTCHour      int
		firstUTCMinute         int
		nextHour, nextMinute   int
		repeatAfter            time.Duration
	}{
		{"new-york/gap", "America/New_York", "*-*-* 02:30", time.March, 8, 7, 0, 2, 30, time.Hour},
		{"new-york/fold", "America/New_York", "*-*-* 01:30", time.November, 1, 5, 30, 1, 30, time.Hour},
		{"berlin/gap", "Europe/Berlin", "*-*-* 02:30", time.March, 29, 1, 0, 2, 30, time.Hour},
		{"berlin/fold", "Europe/Berlin", "*-*-* 02:30", time.October, 25, 0, 30, 2, 30, time.Hour},
		{"lord-howe/gap", "Australia/Lord_Howe", "*-*-* 02:15", time.October, 4, -9, 30, 2, 15, 30 * time.Minute},
		{"lord-howe/fold", "Australia/Lord_Howe", "*-*-* 01:45", time.April, 5, -10, 45, 1, 45, 30 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loc := mustLoc(t, tc.zone)
			fk := NewFake(time.Date(2026, tc.month, tc.day, 0, 0, 0, 0, loc))
			clk := fk.Clock()
			clk.Changed = nil
			var calls atomic.Int32
			e := NewEngine(clk, nil, func(Fire) { calls.Add(1) })
			defer e.Stop()
			spec := Spec{Name: "civil.timer", OnCalendar: []Calendar{mustCal(t, tc.expression)}}
			e.Arm(spec)
			first := time.Date(2026, tc.month, tc.day, tc.firstUTCHour, tc.firstUTCMinute, 0, 0, time.UTC).In(loc)
			waitNext(t, e, spec.Name, first)
			fk.JumpWall(first)
			e.ClockChanged()
			next := time.Date(2026, tc.month, tc.day+1, tc.nextHour, tc.nextMinute, 0, 0, loc)
			waitNext(t, e, spec.Name, next)
			e.fires.Wait()
			if calls.Load() != 1 {
				t.Fatalf("first civil occurrence delivered %d times", calls.Load())
			}
			// For folds this is the second copy of the same civil minute.
			// For gaps it confirms the adjusted occurrence is not replayed.
			fk.JumpWall(first.Add(tc.repeatAfter))
			e.ClockChanged()
			waitNext(t, e, spec.Name, next)
			e.fires.Wait()
			if calls.Load() != 1 {
				t.Fatal("clock reconciliation repeated the civil occurrence")
			}
			fk.JumpWall(next)
			e.ClockChanged()
			e.fires.Wait()
			if calls.Load() != 2 {
				t.Fatal("next day's occurrence was lost")
			}
		})
	}
}
