// Package capability lists the contract names a winunitd build enforces. The
// manager's capability query and the servicing floor read the same lists, so
// a floor evaluates exactly the features the system endpoint reports.
package capability

import (
	"slices"

	"github.com/PLN/winunitd/internal/protocol"
)

// WorkloadFeatures are the contracts every manager endpoint of this build
// enforces, sorted.
func WorkloadFeatures() []string {
	out := append([]string{protocol.FeatureRestartBackoff}, platformWorkloadFeatures()...)
	slices.Sort(out)
	return out
}

// SystemFeatures are the contracts the system endpoint enforces as the
// service runs it, with its user host: the workload contracts plus the
// system-only ones, sorted.
func SystemFeatures() []string {
	out := append(WorkloadFeatures(), platformSystemFeatures()...)
	slices.Sort(out)
	return out
}
