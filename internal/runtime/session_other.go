//go:build !windows

package runtime

import "context"

// InteractiveSessions is empty off Windows.
func InteractiveSessions() ([]uint32, error) {
	return nil, nil
}

// WatchSessions is a no-op off Windows.
func WatchSessions(ctx context.Context, out chan<- SessionChange) {
	_ = ctx
	_ = out
}
