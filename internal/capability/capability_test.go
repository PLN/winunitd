package capability

import (
	"slices"
	"testing"

	"github.com/PLN/winunitd/internal/protocol"
)

func TestFeatureLists(t *testing.T) {
	workload, system := WorkloadFeatures(), SystemFeatures()
	if !slices.IsSorted(workload) || !slices.IsSorted(system) {
		t.Fatalf("unsorted features %v %v", workload, system)
	}
	if !slices.Contains(workload, protocol.FeatureRestartBackoff) {
		t.Fatalf("workload features %v", workload)
	}
	for _, name := range workload {
		if !slices.Contains(system, name) {
			t.Fatalf("system features %v lack workload feature %s", system, name)
		}
	}
	if len(slices.Compact(slices.Clone(system))) != len(system) {
		t.Fatalf("duplicate system features %v", system)
	}
	// Callers get their own slices.
	system[0] = "changed"
	if SystemFeatures()[0] == "changed" {
		t.Fatal("SystemFeatures shares its slice")
	}
}
