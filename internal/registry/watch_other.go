//go:build !windows

package registry

import "fmt"

// OpenWatch is a Linux/non-Windows stub. Registry notification is Windows-only.
func OpenWatch(key Key) (Watch, error) {
	if _, err := ParseKey(key.Raw); err != nil && key.Raw != "" {
		return nil, err
	}
	return nil, fmt.Errorf("registry notification is not supported on this platform")
}
