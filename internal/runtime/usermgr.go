package runtime

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/PLN/winunitd/internal/protocol"
)

const userManagerFlag = "--user-manager"

// UserManagerSpec launches winunitd --user-manager <SID>.
type UserManagerSpec struct {
	SID       string
	Token     *UserToken
	Exe       string
	ExtraArgs []string
	Env       []string
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
	Kill() error
	Wait(ctx context.Context) error
}

// UserManagerLauncher starts a user manager. Production uses
// CreateProcessAsUser with a WTS token.
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

func userManagerEnv(spec UserManagerSpec) []string {
	if spec.Env != nil {
		return spec.Env
	}
	if spec.Token != nil {
		return MergeDeterministicUserEnv(os.Environ(), spec.Token.Info)
	}
	return nil
}

func waitPID(ctx context.Context, alive func() bool, timeout time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !alive() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
