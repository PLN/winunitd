package manager

import (
	"os"
	"path/filepath"
	"time"

	"github.com/PLN/winunitd/internal/eventlog"
	"github.com/PLN/winunitd/internal/notify"
	"github.com/PLN/winunitd/internal/pathwatch"
	"github.com/PLN/winunitd/internal/registry"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/timers"
	"github.com/PLN/winunitd/internal/unit"
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
	// OperationTimeout overrides the aggregate accepted-operation budget.
	// Zero derives a budget from the captured plan, with a five-minute floor.
	// It bounds response waiting; uncancellable native work retains ownership.
	OperationTimeout time.Duration
	// MaxStartTransactions bounds admitted start plans; zero selects 32.
	// Stop/shutdown do not consume this capacity.
	MaxStartTransactions int
	// MaxStopTransactions reserves separate explicit-stop capacity; zero is 32.
	// Manager shutdown and restart's admitted teardown do not consume it.
	MaxStopTransactions int
	// BaseDir is the data root. Production uses C:\ProgramData\winunitd
	// (DESIGN.md §7, §12, §22). Tests pass a temporary directory.
	// Layout: units\, enabled\, journal\.
	BaseDir string
	// Launch starts unit processes. Nil uses runtime.NewLauncher(Daemon).
	Launch runtime.Launcher
	// Daemon is the M4 daemon Job Object. Unit processes nest under it.
	Daemon *runtime.DaemonJob
	// Clock drives the timer scheduler and manager waits for RestartSec,
	// WatchdogSec, TimeoutStartSec, TimeoutStopSec, and operation budgets. Zero uses
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
	// separate; one manager per SID already). Type=scm and
	// Type=scheduled-task are rejected here.
	UserScope bool
	// SessionID returns the Windows session ID to store on journal lines
	// (decimal string). Nil uses runtime.SIDSession(NotifySID) when
	// UserScope. Tests inject a fake. Empty is allowed (linger without
	// a session, or unknown).
	SessionID func() string
	// SCM orchestrates Type=scm proxy units (DESIGN.md §51). Nil uses
	// runtime.DefaultSCM(). It never creates, changes, or deletes SCM
	// configuration. Tests inject a fake so Linux does not call a real SCM.
	SCM runtime.SCM
	// Tasks orchestrates Type=scheduled-task proxy units (DESIGN.md §52).
	// Nil uses runtime.DefaultTaskScheduler(). It never creates, changes,
	// or deletes task definitions. Tests inject a fake so Linux does not
	// call a real Task Scheduler.
	Tasks runtime.TaskScheduler
	// RegistryOpen opens a registry key+subtree watch. Nil uses
	// registry.OpenWatch (Windows RegNotifyChangeKeyValue; stub on Linux).
	// Tests inject a fake so Linux can exercise start-on-change.
	RegistryOpen registry.OpenFunc
	// EventLogOpen opens an Event Log push subscription. Nil uses
	// eventlog.OpenSubscribe (Windows EvtSubscribe; stub on Linux).
	// Tests inject a fake so Linux can exercise start-on-match.
	EventLogOpen eventlog.OpenFunc
	// PathOpen opens a PathChanged= file/directory watch. Nil uses
	// pathwatch.OpenWatch (Windows ReadDirectoryChangesW; stub on Linux).
	// Tests inject a fake so Linux can exercise start-on-change.
	PathOpen pathwatch.OpenFunc
	// PathExists reports whether a PathExists= path exists. Nil uses
	// pathwatch.Exists. Tests inject a stub so Linux can exercise
	// create-to-satisfy without Windows paths on disk.
	PathExists pathwatch.ExistsFunc
	// PathExistsOpen opens a PathExists= creation/deletion watch. Nil uses
	// pathwatch.OpenExistsWatch (Windows ReadDirectoryChangesW on the
	// nearest existing ancestor; stub on Linux). A missing target is OK.
	PathExistsOpen pathwatch.OpenFunc
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

func (c Config) EnabledPath(target, unitName string) string {
	return filepath.Join(c.EnabledDir(), unit.NormalizeName(target), unit.NormalizeName(unitName))
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
