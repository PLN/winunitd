//go:build !windows

package main

import "errors"

// systemStopped cannot verify a stopped system manager off Windows, so a
// floor raise is refused there; tests supply their own check.
func systemStopped() error {
	return errors.New("verifying that the system manager is stopped needs Windows")
}
