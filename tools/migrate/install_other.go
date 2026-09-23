//go:build !windows

package main

import "fmt"

func executeMSISupported() error {
	return fmt.Errorf("msiexec requires Windows")
}

func executeMSI(string) error {
	return fmt.Errorf("msiexec requires Windows")
}

func stopInstalledService() error {
	return fmt.Errorf("stopping the product service requires Windows")
}

func startInstalledService() error {
	return fmt.Errorf("starting the product service requires Windows")
}

func productServiceMatches() error {
	return fmt.Errorf("service identity requires Windows")
}
