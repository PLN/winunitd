package unit

import (
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{in: "5s", want: 5 * time.Second},
		{in: "5", want: 5 * time.Second},
		{in: "5min", want: 5 * time.Minute},
		{in: "1h", want: time.Hour},
		{in: "1h 30min", want: 90 * time.Minute},
		{in: "100ms", want: 100 * time.Millisecond},
		{in: "2d", want: 48 * time.Hour},
		{in: "infinity", want: time.Duration(1<<63 - 1)},
		{in: "", wantErr: true},
		{in: "banana", wantErr: true},
		{in: "5parsecs", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			got, err := parseDuration(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseDuration(%q) succeeded, want error", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseDuration(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("parseDuration(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
