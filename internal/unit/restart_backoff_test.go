package unit

import (
	"testing"
	"time"
)

func TestRestartBackoffFormatAndBounds(t *testing.T) {
	for _, tc := range []struct {
		name, policy string
		valid        bool
	}{
		{"default", "", true},
		{"fixed", "RestartBackoff=fixed\nRestartSec=0\n", true},
		{"exponential", "RestartBackoff=exponential\nRestartSec=2s\nRestartMaxDelaySec=5s\n", true},
		{"default-base", "RestartBackoff=exponential\nRestartMaxDelaySec=1s\n", true},
		{"empty", "RestartBackoff=\n", false},
		{"unknown", "RestartBackoff=random\n", false},
		{"no-cap", "RestartBackoff=exponential\n", false},
		{"unselected-cap", "RestartMaxDelaySec=1s\n", false},
		{"fixed-cap", "RestartBackoff=fixed\nRestartMaxDelaySec=1s\n", false},
		{"zero-base", "RestartBackoff=exponential\nRestartSec=0\nRestartMaxDelaySec=1s\n", false},
		{"small-cap", "RestartBackoff=exponential\nRestartSec=2s\nRestartMaxDelaySec=1s\n", false},
		{"infinite-cap", "RestartBackoff=exponential\nRestartMaxDelaySec=infinity\n", false},
		{"zero-cap", "RestartBackoff=exponential\nRestartMaxDelaySec=0\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := ParseUnit("work.service", "[Unit]\nFormatVersion=2\n[Service]\nExecStart=C:\\Apps\\work.exe\n"+tc.policy)
			if result.HasError() == tc.valid {
				t.Fatalf("policy validity: %+v", result.Issues)
			}
			if tc.name == "exponential" && (result.Unit.Service.RestartBackoff != "exponential" || result.Unit.Service.RestartMaxDelaySec != 5*time.Second) {
				t.Fatal("accepted policy lost its cap")
			}
		})
	}
}
