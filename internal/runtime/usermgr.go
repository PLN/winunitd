package runtime

import (
	"context"
	"fmt"

	"github.com/PLN/winunitd/internal/protocol"
)

const userManagerFlag = "--user-manager"

// UserManagerSpec launches winunitd --user-manager <SID>.
type UserManagerSpec struct {
	SID       string
	Token     *UserToken
	Exe       string
	ExtraArgs []string
	Env       []string // nil resolves the target token's environment without broker inheritance
	Daemon    *DaemonJob
	// cmdArgv, if set, is the full CreateProcessAsUser command (exe +
	// args) and skips UserManagerArgs. Tests use it to launch ping the
	// same way the unit-path listener regression does. Production is nil.
	cmdArgv []string
}

// UserManagerProc is one running per-user manager process.
type UserManagerProc interface {
	PID() int
	SID() string
	Alive() bool
	// Kill confirms process-tree exit before releasing handles; failures retain
	// ownership for retry.
	Kill() error
	Wait(ctx context.Context) error
}

// UserManagerLauncher starts a user manager. Production uses
// CreateProcessAsUser with a WTS token. A non-nil process returned with an error
// transfers unfinished cleanup ownership to the caller, which must retry Kill.
type UserManagerLauncher func(spec UserManagerSpec) (UserManagerProc, error)

// UserManagerArgs is the child command line after exe.
func UserManagerArgs(sid string, extra []string) []string {
	args := []string{userManagerFlag, sid}
	return append(args, extra...)
}

func validateUserManagerSpec(spec UserManagerSpec) error {
	if !protocol.ValidSID(spec.SID) {
		return fmt.Errorf("invalid user manager SID %q", spec.SID)
	}
	if spec.Exe == "" {
		return fmt.Errorf("user manager executable path required")
	}
	return nil
}
