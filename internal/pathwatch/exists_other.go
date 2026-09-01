//go:build !windows

package pathwatch

import "fmt"

// Exists is a Linux/non-Windows stub. Windows paths are not on this disk.
func Exists(s Spec) (bool, error) {
	if _, err := ParseExists(s.Raw); err != nil {
		return false, err
	}
	return false, nil
}

// OpenExistsWatch is a Linux/non-Windows stub. Directory notification is Windows-only.
func OpenExistsWatch(s Spec) (Watch, error) {
	if _, err := ParseExists(s.Raw); err != nil && s.Raw != "" {
		return nil, err
	}
	return nil, fmt.Errorf("path notification is not supported on this platform")
}
