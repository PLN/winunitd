//go:build !windows

package main

import "fmt"

// executeMSISupported rejects a request to run msiexec. A false request
// is success, so the caller can compare the result without a constant nilness.
func executeMSISupported(want bool) error {
	if !want {
		return nil
	}
	return fmt.Errorf("msiexec requires Windows")
}

func executeMSI(string) error {
	return fmt.Errorf("msiexec requires Windows")
}

func stopInstalledService(executed bool) error {
	if !executed {
		return nil
	}
	return fmt.Errorf("stopping the product service requires Windows")
}

func startInstalledService(want bool) error {
	if !want {
		return nil
	}
	return fmt.Errorf("starting the product service requires Windows")
}

func productServiceMatches(check bool) error {
	if !check {
		return nil
	}
	return fmt.Errorf("service identity requires Windows")
}
