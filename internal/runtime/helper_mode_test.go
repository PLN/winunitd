package runtime

import "testing"

func TestHelperModeFrom(t *testing.T) {
	t.Parallel()
	prefix := winunitdHelperArgPrefix
	tests := []struct {
		name string
		args []string
		env  string
		want string
	}{
		{name: "argv1", args: []string{`C:\t.exe`, prefix + "sleep"}, want: "sleep"},
		{name: "argv0 is the flag", args: []string{prefix + "sleep", "-test.run=^$"}, want: "sleep"},
		{name: "glued with -test.run", args: []string{`C:\t.exe`, prefix + "sleep -test.run=^$"}, want: "sleep"},
		{name: "single command line", args: []string{prefix + "sleep -test.run=^$"}, want: "sleep"},
		{name: "env fallback", args: []string{`C:\t.exe`}, env: "oneshot", want: "oneshot"},
		{name: "empty", args: []string{`C:\t.exe`}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := helperModeFrom(tt.args, tt.env); got != tt.want {
				t.Fatalf("helperModeFrom(%q, %q) = %q, want %q", tt.args, tt.env, got, tt.want)
			}
		})
	}
}
