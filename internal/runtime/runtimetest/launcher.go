//go:build !windows

package runtimetest

import "github.com/PLN/winunitd/internal/runtime"

// Launcher is StubLauncher for tests on non-Windows. Production Windows
// binaries do not include the stub (see launcher_windows.go).
func Launcher() runtime.Launcher {
	return runtime.StubLauncher()
}

// LauncherOutput is StubLauncherOutput for tests on non-Windows.
func LauncherOutput(stdout, stderr string) runtime.Launcher {
	return runtime.StubLauncherOutput(stdout, stderr)
}
