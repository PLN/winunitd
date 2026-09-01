package unit

import "testing"

func TestNormalizeName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in, want string
	}{
		{"foo", "foo.service"},
		{"foo.service", "foo.service"},
		{"FOO", "foo.service"},
		{"Foo.SERVICE", "foo.service"},
		{"  Bar.Timer  ", "bar.timer"},
		{"web.target", "web.target"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := NormalizeName(tt.in); got != tt.want {
			t.Errorf("NormalizeName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCompanionServiceNormalized(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in, want string
	}{
		{"foo.timer", "foo.service"},
		{"FOO.TIMER", "foo.service"},
		{"Nightly.registry", "nightly.service"},
		{"App.eventlog", "app.service"},
		{"Watch.path", "watch.service"},
	}
	for _, tt := range tests {
		if got := CompanionService(tt.in); got != tt.want {
			t.Errorf("CompanionService(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
