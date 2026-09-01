//go:build !windows

package eventlog

import "fmt"

// OpenSubscribe is a Linux/non-Windows stub. Event Log subscribe is Windows-only.
func OpenSubscribe(t Trigger) (Subscription, error) {
	if _, err := ParseTrigger(t.Raw); err != nil && t.Raw != "" {
		return nil, err
	}
	return nil, fmt.Errorf("event log subscribe is not supported on this platform")
}
