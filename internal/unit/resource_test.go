package unit

import "testing"

func TestParseMemoryMax(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    uint64
		wantErr bool
	}{
		{in: "2G", want: 2 * 1024 * 1024 * 1024},
		{in: "2g", want: 2 * 1024 * 1024 * 1024},
		{in: "512M", want: 512 * 1024 * 1024},
		{in: "100K", want: 100 * 1024},
		{in: "4096", want: 4096},
		{in: "abc", wantErr: true},
		{in: "2T", wantErr: true},
		{in: "2GB", wantErr: true},
		{in: "0", wantErr: true},
		{in: "0G", wantErr: true},
		{in: "-1M", wantErr: true},
		{in: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			got, err := parseMemoryMax(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseMemoryMax(%q) succeeded, want error", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseMemoryMax(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("parseMemoryMax(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseProcessLimit(t *testing.T) {
	t.Parallel()
	if n, err := parseProcessLimit("32"); err != nil || n != 32 {
		t.Fatalf("32: n=%d err=%v", n, err)
	}
	for _, in := range []string{"0", "-1", "abc", "", "1.5"} {
		if _, err := parseProcessLimit(in); err == nil {
			t.Fatalf("parseProcessLimit(%q) succeeded, want error", in)
		}
	}
}

func TestParsePriorityClass(t *testing.T) {
	t.Parallel()
	ok := []PriorityClass{PriorityIdle, PriorityBelowNormal, PriorityNormal, PriorityAboveNormal, PriorityHigh}
	for _, want := range ok {
		got, err := parsePriorityClass(string(want))
		if err != nil || got != want {
			t.Fatalf("%s: got=%q err=%v", want, got, err)
		}
	}
	got, err := parsePriorityClass("Below-Normal")
	if err != nil || got != PriorityBelowNormal {
		t.Fatalf("case fold: got=%q err=%v", got, err)
	}
	for _, in := range []string{"realtime", "REALTIME", "idle-high", "", "low"} {
		if _, err := parsePriorityClass(in); err == nil {
			t.Fatalf("parsePriorityClass(%q) succeeded, want error", in)
		}
	}
}
