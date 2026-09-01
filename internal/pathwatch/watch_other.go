//go:build !windows

package pathwatch

import "fmt"

// OpenWatch is a Linux/non-Windows stub. Directory notification is Windows-only.
func OpenWatch(s Spec) (Watch, error) {
	if _, err := Parse(s.Raw); err != nil && s.Raw != "" {
		return nil, err
	}
	return nil, fmt.Errorf("path notification is not supported on this platform")
}
