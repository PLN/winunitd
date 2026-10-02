//go:build !windows

package manager

import (
	"slices"
	"testing"

	"github.com/PLN/winunitd/internal/protocol"
)

// Stub builds must not claim native contracts they cannot enforce.
func TestStubCapabilitiesClaimNoNativeContracts(t *testing.T) {
	t.Parallel()
	got := (&Manager{}).Capabilities()
	if !slices.Equal(got.Features, []string{protocol.FeatureRestartBackoff}) || len(got.JobLimits) != 0 || len(got.UserManagerModes) != 0 {
		t.Fatalf("stub capabilities = %v %v %v", got.Features, got.JobLimits, got.UserManagerModes)
	}
}
