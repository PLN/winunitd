//go:build !windows

package runtime

func newLauncher(daemon *DaemonJob) Launcher {
	_ = daemon
	return StubLauncher()
}
