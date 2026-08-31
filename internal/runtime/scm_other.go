//go:build !windows

package runtime

import "context"

// RunningAsService is always false off Windows.
func RunningAsService() (bool, error) {
	return false, nil
}

// Install is only available on Windows.
func Install(exePath, baseDir string) error {
	return errNotWindows
}

// Uninstall is only available on Windows.
func Uninstall() error {
	return errNotWindows
}

// RunHost is only available on Windows.
func RunHost(run func(ctx context.Context) error) error {
	return errNotWindows
}
