//go:build windows

package manager

import "github.com/PLN/winunitd/internal/protocol"

// Windows builds run the stop helper and apply unit Job Object limits on
// every endpoint. The system endpoint also starts lingering users' managers
// through S4U; the named credential-store fallback is experimental.
func platformCapabilities() platformCapability {
	return platformCapability{
		workloadFeatures:      []string{protocol.FeatureExecStop, protocol.FeatureJobLimits},
		jobLimits:             jobLimitDirectives,
		systemFeatures:        []string{protocol.FeatureLingerS4U},
		userModes:             []string{userModeHeadlessS4U, userModeInteractive},
		experimentalUserModes: []string{userModeHeadlessStoreURI},
	}
}
