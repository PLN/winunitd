package unit

import (
	"strings"
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    time.Duration
		wantErr string
	}{
		{name: "seconds suffix", in: "5s", want: 5 * time.Second},
		{name: "bare seconds", in: "5", want: 5 * time.Second},
		{name: "minutes", in: "5min", want: 5 * time.Minute},
		{name: "hours", in: "1h", want: time.Hour},
		{name: "concatenated whitespace", in: "1h 30min", want: 90 * time.Minute},
		{name: "milliseconds", in: "100ms", want: 100 * time.Millisecond},
		{name: "days", in: "2d", want: 48 * time.Hour},
		{name: "infinity", in: "infinity", want: time.Duration(1<<63 - 1)},
		{name: "empty", in: "", wantErr: "empty duration"},
		{name: "garbage", in: "banana", wantErr: "invalid duration"},
		{name: "unknown unit", in: "5parsecs", wantErr: "invalid duration"},
		{name: "fractional", in: "1.5s", want: 1500 * time.Millisecond},
		{name: "space before unit", in: "5 s", want: 5 * time.Second},
		{name: "micro sign us", in: "5µs", want: 5 * time.Microsecond},
		{name: "greek mu us", in: "5μs", want: 5 * time.Microsecond},
		{name: "micro sign with space", in: "5 µs", want: 5 * time.Microsecond},
		{name: "greek mu concatenated", in: "5μs10ms", want: 5*time.Microsecond + 10*time.Millisecond},
		{name: "negative", in: "-5s", wantErr: "invalid duration"},
		{name: "overflow days", in: "99999999999d", wantErr: "overflow"},
		{name: "overflow add", in: "106751d 1d", wantErr: "overflow"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseDuration(tt.in)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("ParseDuration(%q) = %v, want error %q", tt.in, got, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ParseDuration(%q) error %q, want substring %q", tt.in, err.Error(), tt.wantErr)
				}
				if got < 0 {
					t.Fatalf("ParseDuration(%q) returned negative duration %v with error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseDuration(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("ParseDuration(%q) = %v, want %v", tt.in, got, tt.want)
			}
			if got < 0 {
				t.Fatalf("ParseDuration(%q) wrapped to negative %v", tt.in, got)
			}
		})
	}
}
