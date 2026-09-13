package timers

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestMixedTimerWallJumpPreservesRelativeDeadline(t *testing.T) {
	for _, relative := range []string{"boot", "startup"} {
		for _, wall := range []string{"calendar", "active"} {
			t.Run(relative+"/"+wall, func(t *testing.T) {
				fk := NewFake(time.Time{})
				clk := fk.Clock()
				clk.Changed = nil // Explicit reconciliation is the observation barrier.
				var calls atomic.Int32
				e := NewEngine(clk, nil, func(Fire) { calls.Add(1) })
				defer e.Stop()
				spec := Spec{Name: "mixed.timer", Unit: "mixed.service"}
				if relative == "boot" {
					spec.OnBootSec, spec.OnBootSecSet = fk.SinceBoot()+5*time.Minute, true
				} else {
					spec.OnStartupSec, spec.OnStartupSecSet = 5*time.Minute, true
				}
				if wall == "calendar" {
					spec.OnCalendar = []Calendar{mustCal(t, "daily")}
				} else {
					spec.OnUnitActiveSec, spec.OnUnitActiveSecSet = 24*time.Hour, true
				}
				e.Arm(spec)
				if wall == "active" {
					e.UnitActive(spec.Unit, fk.Now())
				}
				waitNext(t, e, spec.Name, fk.Now().Add(5*time.Minute))
				fk.JumpWall(fk.Now().Add(time.Hour))
				e.ClockChanged()
				waitNext(t, e, spec.Name, fk.Now().Add(5*time.Minute))
				e.fires.Wait()
				if got := calls.Load(); got != 0 {
					t.Fatalf("wall jump fired an unelapsed relative deadline %d times", got)
				}
				fk.JumpWall(fk.Now().Add(-2 * time.Hour))
				e.ClockChanged()
				waitNext(t, e, spec.Name, fk.Now().Add(5*time.Minute))
				fk.Advance(5 * time.Minute)
				e.ClockChanged()
				e.fires.Wait()
				if got := calls.Load(); got != 1 {
					t.Fatalf("elapsed relative deadline fired %d times, want one", got)
				}
			})
		}
	}
}

func TestMixedTimerConsumesOnlyDueClockSource(t *testing.T) {
	for _, wall := range []string{"calendar", "active"} {
		t.Run(wall, func(t *testing.T) {
			fk := NewFake(time.Time{})
			clk := fk.Clock()
			clk.Changed = nil
			records := make(chan Runtime, 4)
			store := &Store{save: func(_ string, rt Runtime) error { records <- rt; return nil }}
			e := NewEngine(clk, store, nil)
			defer e.Stop()
			spec := Spec{Name: "mixed.timer", Unit: "mixed.service", OnStartupSec: 5 * time.Minute, OnStartupSecSet: true}
			if wall == "calendar" {
				spec.OnCalendar = []Calendar{mustCal(t, "*-*-* 12:30:00")}
			} else {
				spec.OnUnitActiveSec, spec.OnUnitActiveSecSet = 30*time.Minute, true
			}
			e.Arm(spec)
			e.UnitActive(spec.Unit, fk.Now())
			wallDue := fk.Now().Add(30 * time.Minute)
			waitNext(t, e, spec.Name, fk.Now().Add(5*time.Minute))
			fk.JumpWall(fk.Now().Add(time.Hour))
			e.ClockChanged()
			e.fires.Wait()
			select {
			case rt := <-records:
				if !rt.LastScheduled.Equal(wallDue) || rt.FiredStartup {
					t.Fatalf("wall delivery consumed the wrong source: %+v", rt)
				}
			default:
				t.Fatal("due wall trigger did not fire")
			}
			waitNext(t, e, spec.Name, fk.Now().Add(5*time.Minute))
			fk.Advance(5 * time.Minute)
			e.ClockChanged()
			e.fires.Wait()
			select {
			case rt := <-records:
				if !rt.LastScheduled.Equal(fk.Now()) || !rt.FiredStartup {
					t.Fatalf("relative delivery lost its source: %+v", rt)
				}
			default:
				t.Fatal("wall delivery lost the subsequent relative trigger")
			}
			select {
			case rt := <-records:
				t.Fatalf("duplicate delivery: %+v", rt)
			default:
			}
		})
	}
}
