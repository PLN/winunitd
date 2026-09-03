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

func TestParseCPUWeight(t *testing.T) {
	t.Parallel()
	if n, err := parseCPUWeight("50"); err != nil || n != 50 {
		t.Fatalf("50: n=%d err=%v", n, err)
	}
	if n, err := parseCPUWeight("1"); err != nil || n != 1 {
		t.Fatalf("1: n=%d err=%v", n, err)
	}
	if n, err := parseCPUWeight("10000"); err != nil || n != 10000 {
		t.Fatalf("10000: n=%d err=%v", n, err)
	}
	for _, in := range []string{"0", "-1", "10001", "abc", "", "50%", "1.5"} {
		if _, err := parseCPUWeight(in); err == nil {
			t.Fatalf("parseCPUWeight(%q) succeeded, want error", in)
		}
	}
}

func TestParseCPUQuota(t *testing.T) {
	t.Parallel()
	if n, err := parseCPUQuota("25%"); err != nil || n != 25 {
		t.Fatalf("25%%: n=%d err=%v", n, err)
	}
	if n, err := parseCPUQuota("1%"); err != nil || n != 1 {
		t.Fatalf("1%%: n=%d err=%v", n, err)
	}
	if n, err := parseCPUQuota("100%"); err != nil || n != 100 {
		t.Fatalf("100%%: n=%d err=%v", n, err)
	}
	if n, err := parseCPUQuota(" 100% "); err != nil || n != 100 {
		t.Fatalf(" 100%% : n=%d err=%v", n, err)
	}
	for _, in := range []string{"25", "50", "0%", "101%", "10000%", "abc%", "%", "-1%", "25.5%", "", "25%%"} {
		if _, err := parseCPUQuota(in); err == nil {
			t.Fatalf("parseCPUQuota(%q) succeeded, want error", in)
		}
	}
}

func TestParseIoPriority(t *testing.T) {
	t.Parallel()
	ok := []IoPriority{IoIdle, IoLow, IoNormal, IoHigh}
	for _, want := range ok {
		got, err := parseIoPriority(string(want))
		if err != nil || got != want {
			t.Fatalf("%s: got=%q err=%v", want, got, err)
		}
	}
	got, err := parseIoPriority("Low")
	if err != nil || got != IoLow {
		t.Fatalf("case fold: got=%q err=%v", got, err)
	}
	for _, in := range []string{"below-normal", "realtime", "critical", "", "idle-low"} {
		if _, err := parseIoPriority(in); err == nil {
			t.Fatalf("parseIoPriority(%q) succeeded, want error", in)
		}
	}
}

func TestWindowsCPUWeightMapping(t *testing.T) {
	t.Parallel()
	// weight = clamp(1, 9, (CPUWeight + 1110) / 1111)
	tests := []struct {
		in, want uint32
	}{
		{1, 1},
		{50, 1},
		{1111, 1},
		{1112, 2},
		{2223, 3},
		{5000, 5},
		{8888, 8},
		{8889, 9},
		{10000, 9},
	}
	for _, tt := range tests {
		if got := WindowsCPUWeight(tt.in); got != tt.want {
			t.Fatalf("WindowsCPUWeight(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestWindowsCPURateMapping(t *testing.T) {
	t.Parallel()
	if got := WindowsCPURate(1); got != 100 {
		t.Fatalf("WindowsCPURate(1) = %d, want 100", got)
	}
	if got := WindowsCPURate(25); got != 2500 {
		t.Fatalf("WindowsCPURate(25) = %d, want 2500", got)
	}
	if got := WindowsCPURate(100); got != 10000 {
		t.Fatalf("WindowsCPURate(100) = %d, want 10000", got)
	}
	// A passing parse is N in 1–100, so CpuRate never exceeds 10000.
	for n := uint32(1); n <= 100; n++ {
		if got := WindowsCPURate(n); got > 10000 {
			t.Fatalf("WindowsCPURate(%d) = %d, exceeds 10000", n, got)
		}
	}
}
