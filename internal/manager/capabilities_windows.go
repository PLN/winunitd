//go:build windows

package manager

import "github.com/PLN/winunitd/internal/protocol"

// Windows builds run the stop helper, apply unit Job Object limits, and
// obtain linger tokens through S4U (with the optional store-URI fallback).
func platformCapabilities() platformCapability {
	return platformCapability{
		features:  []string{protocol.FeatureExecStop, protocol.FeatureJobLimits, protocol.FeatureLingerS4U},
		jobLimits: jobLimitDirectives,
		userModes: []string{userModeHeadlessS4U, userModeHeadlessStoreURI, userModeInteractive},
	}
}
