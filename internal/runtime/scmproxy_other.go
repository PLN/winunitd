//go:build !windows

package runtime

import (
	"context"
	"fmt"
	"time"
)

// errSCMNotWindows is returned by the Type=scm stub on non-Windows builds.
// Tests inject a fake SCM; production Linux never talks to a real SCM.
var errSCMNotWindows = fmt.Errorf("Type=scm is only available on Windows")

type stubSCM struct{}

// DefaultSCM returns a stub that does not call a real SCM.
func DefaultSCM() SCM {
	return stubSCM{}
}

func (stubSCM) Start(context.Context, string, time.Duration) (SCMStatus, error) {
	return SCMStatus{}, errSCMNotWindows
}

func (stubSCM) Stop(context.Context, string, time.Duration) (SCMStatus, error) {
	return SCMStatus{}, errSCMNotWindows
}

func (stubSCM) Query(string) (SCMStatus, error) {
	return SCMStatus{}, errSCMNotWindows
}
