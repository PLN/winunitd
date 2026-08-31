package runtime

import (
	"fmt"
	"time"
)

// errNotWindows is returned by SCM install/uninstall on non-Windows builds.
var errNotWindows = fmt.Errorf("Windows Service registration is only available on Windows")

// SCM identity (DESIGN.md §49). Empty ServiceStartName is LocalSystem.
const (
	ServiceName    = "winunitd"
	DisplayName    = "WinUnit Manager"
	ServiceAccount = "LocalSystem"
)

// RecoveryDelay is the SCM restart delay after a failure (DESIGN.md §67).
// Fast recovery is appropriate because winunitd is infrastructure.
const RecoveryDelay = time.Second

// RecoveryActionCount is first / second / subsequent: restart.
const RecoveryActionCount = 3

// RecoveryResetPeriodNever is INFINITE: the failure count is never reset so
// every subsequent failure still restarts the service.
const RecoveryResetPeriodNever = ^uint32(0)

// PreshutdownTimeout is the extra window SCM grants after a preshutdown
// notification (DESIGN.md §42). Ordered unit stop uses this in M10; M4 only
// registers the timeout and accepts the control.
const PreshutdownTimeout = 3 * time.Minute
