package timers

import (
	"testing"
	"time"
	_ "time/tzdata"
)

func TestParseCalendar(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		wantErr bool
		check   func(t *testing.T, c Calendar)
	}{
		{
			name: "daily",
			in:   "daily",
			check: func(t *testing.T, c Calendar) {
				if c.Hour != 0 || c.Minute != 0 || c.Second != 0 {
					t.Fatalf("daily should be midnight, got %02d:%02d:%02d", c.Hour, c.Minute, c.Second)
				}
				if c.Year != -1 || c.Month != -1 || c.Day != -1 {
					t.Fatalf("daily should wildcard the date, got %d-%d-%d", c.Year, c.Month, c.Day)
				}
				if c.RestrictWeekdays {
					t.Fatal("daily should match every weekday")
				}
			},
		},
		{
			name: "weekdays morning",
			in:   "Mon..Fri 03:00",
			check: func(t *testing.T, c Calendar) {
				if c.Hour != 3 || c.Minute != 0 || c.Second != 0 {
					t.Fatalf("time = %02d:%02d:%02d", c.Hour, c.Minute, c.Second)
				}
				for _, d := range []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday} {
					if !c.AllowsWeekday(d) {
						t.Fatalf("expected %s", d)
					}
				}
				if c.AllowsWeekday(time.Saturday) || c.AllowsWeekday(time.Sunday) {
					t.Fatal("weekend should be excluded")
				}
			},
		},
		{
			name: "star date with seconds",
			in:   "*-*-* 15:04:05",
			check: func(t *testing.T, c Calendar) {
				if c.Hour != 15 || c.Minute != 4 || c.Second != 5 {
					t.Fatalf("time = %02d:%02d:%02d", c.Hour, c.Minute, c.Second)
				}
				if c.Year != -1 || c.Month != -1 || c.Day != -1 {
					t.Fatalf("date = %d-%d-%d", c.Year, c.Month, c.Day)
				}
				if c.RestrictWeekdays {
					t.Fatal("*-*-* should match every weekday")
				}
			},
		},
		{
			name: "star date without seconds",
			in:   "*-*-* 07:30",
			check: func(t *testing.T, c Calendar) {
				if c.Hour != 7 || c.Minute != 30 || c.Second != 0 {
					t.Fatalf("time = %02d:%02d:%02d", c.Hour, c.Minute, c.Second)
				}
			},
		},
		{
			name: "comma weekdays",
			in:   "Mon,Wed,Fri 09:00",
			check: func(t *testing.T, c Calendar) {
				if !c.AllowsWeekday(time.Monday) || !c.AllowsWeekday(time.Wednesday) || !c.AllowsWeekday(time.Friday) {
					t.Fatal("expected Mon,Wed,Fri")
				}
				if c.AllowsWeekday(time.Tuesday) {
					t.Fatal("Tuesday should be excluded")
				}
			},
		},
		{name: "empty", in: "", wantErr: true},
		{name: "garbage", in: "whenever", wantErr: true},
		{name: "bad hour", in: "Mon..Fri 25:00", wantErr: true},
		{name: "bad weekday", in: "Mon..Foo 03:00", wantErr: true},
		{name: "time only", in: "03:00", wantErr: true},
		{name: "weekly alias unsupported", in: "weekly", wantErr: true},
		{name: "bad seconds", in: "*-*-* 12:00:99", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseCalendar(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseCalendar(%q) succeeded, want error", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseCalendar(%q): %v", tt.in, err)
			}
			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}

func TestCalendarNextPrevious(t *testing.T) {
	t.Parallel()
	loc := time.FixedZone("test", 0)
	from := time.Date(2026, 8, 31, 12, 0, 0, 0, loc) // Monday

	daily, err := ParseCalendar("daily")
	if err != nil {
		t.Fatal(err)
	}
	next := daily.Next(from)
	wantNext := time.Date(2026, 9, 1, 0, 0, 0, 0, loc)
	if !next.Equal(wantNext) {
		t.Fatalf("daily next = %v, want %v", next, wantNext)
	}
	prev := daily.Previous(from)
	wantPrev := time.Date(2026, 8, 31, 0, 0, 0, 0, loc)
	if !prev.Equal(wantPrev) {
		t.Fatalf("daily previous = %v, want %v", prev, wantPrev)
	}

	tod, err := ParseCalendar("*-*-* 15:04:05")
	if err != nil {
		t.Fatal(err)
	}
	next = tod.Next(from)
	wantNext = time.Date(2026, 8, 31, 15, 4, 5, 0, loc)
	if !next.Equal(wantNext) {
		t.Fatalf("*-*-* next = %v, want %v", next, wantNext)
	}
	afterSlot := time.Date(2026, 8, 31, 15, 4, 5, 0, loc)
	next = tod.Next(afterSlot)
	wantNext = time.Date(2026, 9, 1, 15, 4, 5, 0, loc)
	if !next.Equal(wantNext) {
		t.Fatalf("strictly after slot: %v, want %v", next, wantNext)
	}

	weekdays, err := ParseCalendar("Mon..Fri 03:00")
	if err != nil {
		t.Fatal(err)
	}
	// Monday 12:00 -> Tuesday 03:00
	next = weekdays.Next(from)
	wantNext = time.Date(2026, 9, 1, 3, 0, 0, 0, loc)
	if !next.Equal(wantNext) {
		t.Fatalf("Mon..Fri next = %v, want %v", next, wantNext)
	}
	fridayNight := time.Date(2026, 9, 4, 12, 0, 0, 0, loc) // Friday
	next = weekdays.Next(fridayNight)
	wantNext = time.Date(2026, 9, 7, 3, 0, 0, 0, loc) // Monday
	if !next.Equal(wantNext) {
		t.Fatalf("weekend skip: %v, want %v", next, wantNext)
	}
}

func mustCal(t *testing.T, expr string) Calendar {
	t.Helper()
	c, err := ParseCalendar(expr)
	if err != nil {
		t.Fatalf("ParseCalendar(%q): %v", expr, err)
	}
	return c
}

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q): %v", name, err)
	}
	return loc
}

func civil(loc *time.Location, y int, m time.Month, d, h, min, s int) time.Time {
	return time.Date(y, m, d, h, min, s, 0, loc)
}

// utcInstant is a zone-independent expected instant (DST first-occurrence / gap).
func utcInstant(loc *time.Location, y int, m time.Month, d, h, min, s int) time.Time {
	return time.Date(y, m, d, h, min, s, 0, time.UTC).In(loc)
}

func TestCalendarCivilDateAndDST(t *testing.T) {
	t.Parallel()

	for _, zone := range []string{"America/New_York", "Europe/Berlin"} {
		zone := zone
		t.Run(zone, func(t *testing.T) {
			t.Parallel()
			loc := mustLoc(t, zone)

			type tc struct {
				name string
				expr string
				from time.Time
				next time.Time // zero means no match
				prev time.Time
			}
			cases := []tc{
				{
					name: "*-*-31 from Feb does not overflow to Mar 3",
					expr: "*-*-31 03:00",
					from: civil(loc, 2026, 2, 10, 12, 0, 0),
					next: civil(loc, 2026, 3, 31, 3, 0, 0),
					prev: civil(loc, 2026, 1, 31, 3, 0, 0),
				},
				{
					name: "*-*-31 from April skips April 31 (not May 1)",
					expr: "*-*-31 03:00",
					from: civil(loc, 2026, 4, 10, 12, 0, 0),
					next: civil(loc, 2026, 5, 31, 3, 0, 0),
					prev: civil(loc, 2026, 3, 31, 3, 0, 0),
				},
				{
					name: "*-2-29 non-leap skips to next leap year",
					expr: "*-2-29 03:00",
					from: civil(loc, 2026, 2, 10, 12, 0, 0),
					next: civil(loc, 2028, 2, 29, 3, 0, 0),
					prev: civil(loc, 2024, 2, 29, 3, 0, 0),
				},
				{
					name: "2027-*-* from prior year is Jan 1 not Sep 1",
					expr: "2027-*-* 03:00",
					from: civil(loc, 2026, 9, 1, 12, 0, 0),
					next: civil(loc, 2027, 1, 1, 3, 0, 0),
				},
				{
					name: "*-12-* from January is Dec 1 not Dec 15",
					expr: "*-12-* 03:00",
					from: civil(loc, 2026, 1, 15, 12, 0, 0),
					next: civil(loc, 2026, 12, 1, 3, 0, 0),
					prev: civil(loc, 2025, 12, 31, 3, 0, 0),
				},
			}

			if zone == "America/New_York" {
				// 2026-03-08 02:00 EST → 03:00 EDT. 02:30 does not exist.
				// First valid after the gap is 03:00 EDT = 07:00 UTC.
				// 2026-11-01 02:00 EDT → 01:00 EST. 01:30 occurs twice;
				// first is 01:30 EDT = 05:30 UTC.
				gapDay := civil(loc, 2026, 3, 8, 0, 0, 0)
				gapFire := utcInstant(loc, 2026, 3, 8, 7, 0, 0)
				foldFirst := utcInstant(loc, 2026, 11, 1, 5, 30, 0)
				cases = append(cases,
					tc{
						name: "spring-forward 02:30 fires at first valid 03:00",
						expr: "*-*-* 02:30",
						from: gapDay,
						next: gapFire,
						prev: civil(loc, 2026, 3, 7, 2, 30, 0),
					},
					tc{
						name: "spring-forward Previous later that day is the gap instant",
						expr: "*-*-* 02:30",
						from: civil(loc, 2026, 3, 8, 12, 0, 0),
						next: civil(loc, 2026, 3, 9, 2, 30, 0),
						prev: gapFire,
					},
					tc{
						name: "fall-back 01:30 is the first occurrence",
						expr: "*-*-* 01:30",
						from: civil(loc, 2026, 11, 1, 0, 0, 0),
						next: foldFirst,
						prev: civil(loc, 2026, 10, 31, 1, 30, 0),
					},
					tc{
						name: "fall-back Next after first occurrence skips the second copy",
						expr: "*-*-* 01:30",
						from: foldFirst,
						next: civil(loc, 2026, 11, 2, 1, 30, 0),
						prev: civil(loc, 2026, 10, 31, 1, 30, 0),
					},
				)
			} else {
				// 2026-03-29 02:00 CET → 03:00 CEST. 02:30 does not exist.
				// First valid after the gap is 03:00 CEST = 01:00 UTC.
				// 2026-10-25 03:00 CEST → 02:00 CET. 02:30 occurs twice;
				// first is 02:30 CEST = 00:30 UTC.
				gapDay := civil(loc, 2026, 3, 29, 0, 0, 0)
				gapFire := utcInstant(loc, 2026, 3, 29, 1, 0, 0)
				foldFirst := utcInstant(loc, 2026, 10, 25, 0, 30, 0)
				cases = append(cases,
					tc{
						name: "spring-forward 02:30 fires at first valid 03:00",
						expr: "*-*-* 02:30",
						from: gapDay,
						next: gapFire,
						prev: civil(loc, 2026, 3, 28, 2, 30, 0),
					},
					tc{
						name: "spring-forward Previous later that day is the gap instant",
						expr: "*-*-* 02:30",
						from: civil(loc, 2026, 3, 29, 12, 0, 0),
						next: civil(loc, 2026, 3, 30, 2, 30, 0),
						prev: gapFire,
					},
					tc{
						name: "fall-back 02:30 is the first occurrence",
						expr: "*-*-* 02:30",
						from: civil(loc, 2026, 10, 25, 0, 0, 0),
						next: foldFirst,
						prev: civil(loc, 2026, 10, 24, 2, 30, 0),
					},
					tc{
						name: "fall-back Next after first occurrence skips the second copy",
						expr: "*-*-* 02:30",
						from: foldFirst,
						next: civil(loc, 2026, 10, 26, 2, 30, 0),
						prev: civil(loc, 2026, 10, 24, 2, 30, 0),
					},
				)
			}

			for _, tt := range cases {
				tt := tt
				t.Run(tt.name, func(t *testing.T) {
					t.Parallel()
					cal := mustCal(t, tt.expr)
					gotNext := cal.Next(tt.from)
					if tt.next.IsZero() {
						if !gotNext.IsZero() {
							t.Fatalf("Next(%v) = %v, want zero", tt.from, gotNext)
						}
					} else if !gotNext.Equal(tt.next) {
						t.Fatalf("Next(%v) = %v (unix %d), want %v (unix %d)",
							tt.from, gotNext, gotNext.Unix(), tt.next, tt.next.Unix())
					}
					gotPrev := cal.Previous(tt.from)
					if tt.prev.IsZero() {
						if !gotPrev.IsZero() {
							t.Fatalf("Previous(%v) = %v, want zero", tt.from, gotPrev)
						}
					} else if !gotPrev.Equal(tt.prev) {
						t.Fatalf("Previous(%v) = %v (unix %d), want %v (unix %d)",
							tt.from, gotPrev, gotPrev.Unix(), tt.prev, tt.prev.Unix())
					}
				})
			}
		})
	}
}

func TestCalendarFullySpecifiedInvalidDay(t *testing.T) {
	t.Parallel()
	loc := mustLoc(t, "America/New_York")
	cal := mustCal(t, "2026-02-31 03:00")
	from := civil(loc, 2026, 1, 1, 0, 0, 0)
	if next := cal.Next(from); !next.IsZero() {
		t.Fatalf("Next(2026-02-31) = %v, want zero (overflow rejected)", next)
	}
	if prev := cal.Previous(civil(loc, 2026, 6, 1, 0, 0, 0)); !prev.IsZero() {
		t.Fatalf("Previous(2026-02-31) = %v, want zero", prev)
	}
}

func TestCalendarFallBackIsFirstOccurrenceNotSecond(t *testing.T) {
	t.Parallel()
	ny := mustLoc(t, "America/New_York")
	cal := mustCal(t, "*-*-* 01:30")
	first := utcInstant(ny, 2026, 11, 1, 5, 30, 0)
	second := utcInstant(ny, 2026, 11, 1, 6, 30, 0)
	if !first.Before(second) {
		t.Fatalf("expected first %v before second %v", first, second)
	}
	_, off1 := first.Zone()
	_, off2 := second.Zone()
	if off1 == off2 {
		t.Fatalf("expected distinct offsets for the two 01:30 copies, both %d", off1)
	}
	got := cal.Next(civil(ny, 2026, 11, 1, 0, 0, 0))
	if !got.Equal(first) {
		t.Fatalf("Next = %v unix %d, want first occurrence %v unix %d (not second unix %d)",
			got, got.Unix(), first, first.Unix(), second.Unix())
	}
	if got.Equal(second) {
		t.Fatal("returned the second fall-back occurrence")
	}

	ber := mustLoc(t, "Europe/Berlin")
	cal = mustCal(t, "*-*-* 02:30")
	first = utcInstant(ber, 2026, 10, 25, 0, 30, 0)
	second = utcInstant(ber, 2026, 10, 25, 1, 30, 0)
	got = cal.Next(civil(ber, 2026, 10, 25, 0, 0, 0))
	if !got.Equal(first) {
		t.Fatalf("Berlin Next = %v unix %d, want first %v unix %d (not second unix %d)",
			got, got.Unix(), first, first.Unix(), second.Unix())
	}
}
