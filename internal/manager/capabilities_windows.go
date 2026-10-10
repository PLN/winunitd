//go:build windows

package manager

// Windows builds apply unit Job Object limits on every endpoint. The system
// endpoint also starts lingering users' managers through S4U; the named
// credential-store fallback is experimental. Feature names come from the
// capability package.
func platformCapabilities() platformCapability {
	return platformCapability{
		jobLimits:             jobLimitDirectives,
		userModes:             []string{userModeHeadlessS4U, userModeInteractive},
		experimentalUserModes: []string{userModeHeadlessStoreURI},
	}
}
