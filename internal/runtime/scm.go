package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

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
// notification (DESIGN.md §42). Ordered unit stop uses this window.
const PreshutdownTimeout = 3 * time.Minute

// StopPendingWaitHint is the SCM WaitHint during ordered unit stop.
// It matches PreshutdownTimeout so a plain `sc stop` does not flag the
// service hung while units drain (issue #33). Aggregate TimeoutStopSec
// for a running graph is bounded by this window.
const StopPendingWaitHint = PreshutdownTimeout

// DataDirNames are created under the install base directory (DESIGN.md §7, §48).
// units, enabled, journal, runtime. PATH and Event Log provider are not touched.
var DataDirNames = []string{"units", "enabled", "journal", "runtime", "linger"}

// EnsureDataDirs creates baseDir and the MVP data subdirectories if missing.
func EnsureDataDirs(baseDir string) error {
	if baseDir == "" {
		return fmt.Errorf("base directory required")
	}
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return err
	}
	for _, name := range DataDirNames {
		if err := os.MkdirAll(filepath.Join(baseDir, name), 0o755); err != nil {
			return err
		}
	}
	return nil
}
