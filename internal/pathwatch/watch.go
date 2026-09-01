package pathwatch

import "errors"

// ErrMissingPath is returned when the watched path does not exist.
var ErrMissingPath = errors.New("path is missing")

// ErrUnwatchable is returned when the path exists but cannot be watched.
var ErrUnwatchable = errors.New("path is not watchable")

// Watch is one PathChanged= notification. C receives a signal on each
// matching change. Close stops the watch. Implementations must be safe
// for concurrent Close.
type Watch interface {
	C() <-chan struct{}
	Close() error
}

// OpenFunc opens a watch on one PathChanged= or PathExists= path.
type OpenFunc func(Spec) (Watch, error)

// ExistsFunc reports whether a PathExists= path exists.
type ExistsFunc func(Spec) (bool, error)
