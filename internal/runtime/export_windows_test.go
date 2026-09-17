//go:build windows

package runtime

import "testing"

// The external test package may import manager without a cycle. These expose
// the native token fixture and the suspended-stage seam to it only.
func NativeTestUserToken(t *testing.T) *UserToken { return testUserToken(t) }

func StartUserManagerSuspended(spec UserManagerSpec, suspended func(pid int)) (UserManagerProc, error) {
	spec.suspended = suspended
	return StartUserManager(spec)
}
