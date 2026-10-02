//go:build windows

package manager

import (
	"slices"
	"testing"

	"github.com/PLN/winunitd/internal/protocol"
)

func TestWindowsCapabilitiesAdvertiseNativeContracts(t *testing.T) {
	t.Parallel()
	got := (&Manager{}).Capabilities()
	want := []string{protocol.FeatureExecStop, protocol.FeatureJobLimits, protocol.FeatureLingerS4U, protocol.FeatureRestartBackoff}
	if !slices.Equal(got.Features, want) {
		t.Fatalf("features = %v, want %v", got.Features, want)
	}
	if !slices.Equal(got.JobLimits, jobLimitDirectives) {
		t.Fatalf("jobLimits = %v", got.JobLimits)
	}
	modes := []string{userModeHeadlessS4U, userModeHeadlessStoreURI, userModeInteractive}
	if !slices.Equal(got.UserManagerModes, modes) {
		t.Fatalf("userManagerModes = %v", got.UserManagerModes)
	}
}
