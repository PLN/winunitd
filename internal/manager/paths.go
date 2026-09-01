package manager

import (
	"os"
	"path/filepath"

	"github.com/PLN/winunitd/internal/eventlog"
	"github.com/PLN/winunitd/internal/notify"
	"github.com/PLN/winunitd/internal/registry"
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
	// Clock drives the timer scheduler and manager waits for RestartSec,
	// WatchdogSec, TimeoutStartSec, and TimeoutStopSec. Zero uses
	// timers.DefaultClock(). Tests inject timers.Fake so Advance fires
	// those waits without sleeping on the real clock (issue #28).
	Clock timers.Clock
	// HasInteractiveSession reports whether a suitable interactive
	// session exists for this manager. Nil means yes (system manager
	// and tests that do not care). User managers set this so
	// RequiresInteractiveSession=yes units skip when headless, and so
	// graphical-session.target can be driven from SIDHasInteractiveSession.
	HasInteractiveSession func() bool
	// NotifyListen opens the per-unit notify pipe. Nil uses notify.Listen
	// (named pipe on Windows, fake TCP on Linux).
	NotifyListen notify.ListenFunc
	// NotifySID is the unit-process user SID for the notify pipe DACL.
	// Empty uses the current process user on Windows.
	NotifySID string
	// UserScope is true for a per-user manager (winunitd --user-manager).
	// It loads graphical-session.target and starts/stops it from session
	// tracking. The system manager stays false (identity vs session stay
	// separate; one manager per SID already). Type=scm is rejected here.
	UserScope bool
	// SCM orchestrates Type=scm proxy units (DESIGN.md §51). Nil uses
	// runtime.DefaultSCM(). It never creates, changes, or deletes SCM
	// configuration. Tests inject a fake so Linux does not call a real SCM.
	SCM runtime.SCM
	// RegistryOpen opens a registry key+subtree watch. Nil uses
	// registry.OpenWatch (Windows RegNotifyChangeKeyValue; stub on Linux).
	// Tests inject a fake so Linux can exercise start-on-change.
	RegistryOpen registry.OpenFunc
	// EventLogOpen opens an Event Log push subscription. Nil uses
	// eventlog.OpenSubscribe (Windows EvtSubscribe; stub on Linux).
	// Tests inject a fake so Linux can exercise start-on-match.
	EventLogOpen eventlog.OpenFunc
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
