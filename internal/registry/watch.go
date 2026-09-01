package registry

import "errors"

// ErrMissingKey is returned when the watched key does not exist.
var ErrMissingKey = errors.New("registry key is missing")

// Watch is one key+subtree notification. C receives a signal on each change.
// Close stops the watch. Implementations must be safe for concurrent Close.
type Watch interface {
	C() <-chan struct{}
	Close() error
}

// OpenFunc opens a watch on a key and its subtree.
type OpenFunc func(Key) (Watch, error)
