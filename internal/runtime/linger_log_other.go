//go:build !windows

package runtime

// SetLingerLogf is a no-op without native linger acquisition.
func SetLingerLogf(func(string, ...any)) {}
