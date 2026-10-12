//go:build windows

package capability

import "github.com/PLN/winunitd/internal/protocol"

// Windows builds run the stop helper and apply unit Job Object limits on
// every endpoint. The system endpoint also starts lingering users' managers
// through S4U.
func platformWorkloadFeatures() []string {
	return []string{protocol.FeatureExecStop, protocol.FeatureJobLimits}
}

func platformSystemFeatures() []string {
	return []string{protocol.FeatureLingerS4U}
}
