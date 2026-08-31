package core

import "testing"

func TestStateString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		s    State
		want string
	}{
		{Inactive, "inactive"},
		{Activating, "activating"},
		{Active, "active"},
		{Deactivating, "deactivating"},
		{Failed, "failed"},
	}
	for _, tt := range tests {
		if got := tt.s.String(); got != tt.want {
			t.Errorf("%d.String() = %q, want %q", tt.s, got, tt.want)
		}
	}
}
