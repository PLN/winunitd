package manager

import (
	"os"
	"path/filepath"

	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/timers"
)

const (
	unitsDirName   = "units"
	enabledDirName = "enabled"
	journalDirName = "journal"
	runtimeDirName = "runtime"
	lingerDirName  = "linger"
)

// Config is the on-disk layout for a manager instance.
type Config struct {
	// BaseDir is the data root. Production uses C:\ProgramData\winunitd
	// (DESIGN.md §7, §12, §22). Tests pass a temporary directory.
	// Layout: units\, enabled\, journal\.
	BaseDir string
	// Launch starts unit processes. Nil uses runtime.NewLauncher(Daemon).
	Launch runtime.Launcher
	// Daemon is the M4 daemon Job Object. Unit processes nest under it.
	Daemon *runtime.DaemonJob
	// Clock drives the timer scheduler. Zero uses timers.DefaultClock().
	Clock timers.Clock
	// HasInteractiveSession reports whether a suitable interactive
	// session exists for this manager. Nil means yes (system manager
	// and tests that do not care). User managers set this so
	// RequiresInteractiveSession=yes units skip when headless, and so
	// graphical-session.target can be driven from SIDHasInteractiveSession.
	HasInteractiveSession func() bool
	// UserScope is true for a per-user manager (winunitd --user-manager).
	// It loads graphical-session.target and starts/stops it from session
	// tracking. The system manager stays false (identity vs session stay
	// separate; one manager per SID already).
	UserScope bool
}

// DefaultBaseDir is C:\ProgramData\winunitd when ProgramData is set.
func DefaultBaseDir() string {
	if pd := os.Getenv("ProgramData"); pd != "" {
		return filepath.Join(pd, "winunitd")
	}
	return `C:\ProgramData\winunitd`
}

// DefaultUserBaseDir is %LOCALAPPDATA%\winunitd (DESIGN.md §7).
func DefaultUserBaseDir() string {
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		return filepath.Join(la, "winunitd")
	}
	if up := os.Getenv("USERPROFILE"); up != "" {
		return filepath.Join(up, "AppData", "Local", "winunitd")
	}
	return filepath.Join(os.TempDir(), "winunitd")
}

func (c Config) UnitsDir() string {
	return filepath.Join(c.BaseDir, unitsDirName)
}

func (c Config) EnabledDir() string {
	return filepath.Join(c.BaseDir, enabledDirName)
}

func (c Config) EnabledPath(target, unit string) string {
	return filepath.Join(c.EnabledDir(), target, unit)
}

func (c Config) JournalDir() string {
	return filepath.Join(c.BaseDir, journalDirName)
}

func (c Config) RuntimeDir() string {
	return filepath.Join(c.BaseDir, runtimeDirName)
}

func (c Config) TimerStateDir() string {
	return filepath.Join(c.RuntimeDir(), "timers")
}

func (c Config) LingerDir() string {
	return filepath.Join(c.BaseDir, lingerDirName)
}
