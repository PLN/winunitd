//go:build windows

package runtime

import (
	"sync"
	"testing"
)

// The external test package may import manager without a cycle. These expose
// the native token fixture and the suspended-stage seam to it only.
func NativeTestUserToken(t *testing.T) *UserToken { return testUserToken(t) }

func StartUserManagerSuspended(spec UserManagerSpec, suspended func(pid int)) (UserManagerProc, error) {
	spec.suspended = suspended
	return StartUserManager(spec)
}

var nativeOverlapBroker struct {
	once sync.Once
	job  *DaemonJob
	err  error
}

// NativeOverlapBroker retains one root for the dedicated fixture process.
// Closing and replacing a self-assigned job does not remove its membership;
// later same-session children would otherwise inherit obsolete nested roots.
// TestMain closes this handle after every case has drained its own resources.
func NativeOverlapBroker() (*DaemonJob, error) {
	nativeOverlapBroker.once.Do(func() {
		nativeOverlapBroker.job, nativeOverlapBroker.err = OpenBrokerJob()
		if nativeOverlapBroker.err == nil {
			nativeOverlapBroker.err = nativeOverlapBroker.job.AssignSelf()
		}
	})
	return nativeOverlapBroker.job, nativeOverlapBroker.err
}
