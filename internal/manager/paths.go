package manager

import (
	"os"
	"path/filepath"
)

const (
	unitsDirName   = "units"
	enabledDirName = "enabled"
	defaultTarget  = "default.target"
)

// Config is the on-disk layout for a manager instance.
type Config struct {
	// BaseDir is the data root. Production uses C:\ProgramData\winunitd
	// (DESIGN.md §7, §12). Tests pass a temporary directory.
	BaseDir string
}

// DefaultBaseDir is C:\ProgramData\winunitd when ProgramData is set.
func DefaultBaseDir() string {
	if pd := os.Getenv("ProgramData"); pd != "" {
		return filepath.Join(pd, "winunitd")
	}
	return `C:\ProgramData\winunitd`
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
