package manager

// Cleanup authority is independent for each owned resource class. Successful
// process termination cannot erase a failed notification or watch close.
type cleanupResources uint8

const (
	cleanupWorkload cleanupResources = 1 << iota // process/job or native proxy
	cleanupNotify
	cleanupWatch
	cleanupHelper
	cleanupJournal
)

// Called only by serialized lifecycle decisions, after exact-owner validation.
func (rt *unitRuntime) setCleanup(resource cleanupResources, pending bool) {
	if pending {
		rt.cleanup |= resource
	} else {
		rt.cleanup &^= resource
	}
}

func (rt *unitRuntime) cleanupPending() bool { return rt != nil && rt.cleanup != 0 }

func (rt *unitRuntime) pendingCleanupNames() []string {
	var names []string
	for _, resource := range []struct {
		bit  cleanupResources
		name string
	}{
		{cleanupWorkload, "workload"}, {cleanupNotify, "notification"}, {cleanupWatch, "watch"},
		{cleanupHelper, "stop-helper"}, {cleanupJournal, "journal"},
	} {
		if rt.cleanup&resource.bit != 0 {
			names = append(names, resource.name)
		}
	}
	return names
}
