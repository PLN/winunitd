//go:build !windows

package main

// brokerStopped has no broker to check off Windows.
func brokerStopped() error { return nil }
