//go:build !windows

package manager

// Non-Windows builds are test stand-ins: launches, helpers, Job Objects,
// and S4U logons are stubs, so none of those contracts is enforced.
func platformCapabilities() platformCapability {
	return platformCapability{}
}
