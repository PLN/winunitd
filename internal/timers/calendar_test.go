package timers

import (
	"testing"
	"time"
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
