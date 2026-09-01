package journal

import (
	"testing"
	"time"
)

func TestParseSince(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 1, 15, 0, 0, 0, time.UTC)
	rfc := "2026-08-31T10:24:11Z"
	wantRFC, err := time.Parse(time.RFC3339, rfc)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		in      string
		want    time.Time
		wantErr bool
	}{
		{in: rfc, want: wantRFC},
		{in: "2026-08-31T10:24:11.123456789Z", want: time.Date(2026, 8, 31, 10, 24, 11, 123456789, time.UTC)},
		{in: "2026-01-02", want: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
		{in: "1 hour ago", want: now.Add(-time.Hour)},
		{in: "2 hours ago", want: now.Add(-2 * time.Hour)},
		{in: "30 minutes ago", want: now.Add(-30 * time.Minute)},
		{in: "1h", want: now.Add(-time.Hour)},
		{in: "90m", want: now.Add(-90 * time.Minute)},
		{in: "", wantErr: true},
		{in: "bogus", wantErr: true},
		{in: "1 fortnight ago", wantErr: true},
	}
	for _, tc := range tests {
		got, err := ParseSince(tc.in, now)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseSince(%q) = %v, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseSince(%q): %v", tc.in, err)
			continue
		}
		if !got.Equal(tc.want) {
			t.Errorf("ParseSince(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
