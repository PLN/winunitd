//go:build !windows

package manager

import (
	"slices"
	"testing"

	"github.com/PLN/winunitd/internal/protocol"
)

// Stub builds must not claim native contracts they cannot enforce, on either
// endpoint.
func TestStubCapabilitiesClaimNoNativeContracts(t *testing.T) {
	t.Parallel()
	for _, got := range []*protocol.CapabilitiesResult{(&Manager{}).Capabilities(), (&Control{Units: &Manager{}, Users: &UserHost{}}).Capabilities()} {
		if !slices.Equal(got.Features, []string{protocol.FeatureRestartBackoff}) || len(got.JobLimits) != 0 ||
			len(got.UserManagerModes) != 0 || len(got.ExperimentalUserManagerModes) != 0 {
			t.Fatalf("stub capabilities = %v %v %v %v", got.Features, got.JobLimits, got.UserManagerModes, got.ExperimentalUserManagerModes)
		}
	}
}
