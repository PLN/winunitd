//go:build windows

package manager

import (
	"slices"
	"testing"

	"github.com/PLN/winunitd/internal/protocol"
)

func TestWindowsCapabilitiesAdvertiseNativeContracts(t *testing.T) {
	t.Parallel()
	workload := []string{protocol.FeatureExecStop, protocol.FeatureJobLimits, protocol.FeatureRestartBackoff}
	user := (&Manager{cfg: Config{UserScope: true}}).Capabilities()
	if !slices.Equal(user.Features, workload) || !slices.Equal(user.JobLimits, jobLimitDirectives) ||
		len(user.UserManagerModes) != 0 || len(user.ExperimentalUserManagerModes) != 0 {
		t.Fatalf("user endpoint = %v %v %v %v", user.Features, user.JobLimits, user.UserManagerModes, user.ExperimentalUserManagerModes)
	}
	bare := (&Control{Units: &Manager{}}).Capabilities()
	if !slices.Equal(bare.Features, workload) || len(bare.UserManagerModes) != 0 {
		t.Fatalf("system endpoint without a user host = %v %v", bare.Features, bare.UserManagerModes)
	}
	system := (&Control{Units: &Manager{}, Users: &UserHost{}}).Capabilities()
	want := []string{protocol.FeatureExecStop, protocol.FeatureJobLimits, protocol.FeatureLingerS4U, protocol.FeatureRestartBackoff}
	if !slices.Equal(system.Features, want) || !slices.Equal(system.JobLimits, jobLimitDirectives) ||
		!slices.Equal(system.UserManagerModes, []string{userModeHeadlessS4U, userModeInteractive}) ||
		!slices.Equal(system.ExperimentalUserManagerModes, []string{userModeHeadlessStoreURI}) {
		t.Fatalf("system endpoint = %v %v %v %v", system.Features, system.JobLimits, system.UserManagerModes, system.ExperimentalUserManagerModes)
	}
}
