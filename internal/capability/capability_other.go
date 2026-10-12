//go:build !windows

package capability

// Non-Windows builds are test stand-ins: launches, helpers, Job Objects and
// S4U logons are stubs, so none of those contracts is enforced.
func platformWorkloadFeatures() []string { return nil }

func platformSystemFeatures() []string { return nil }
