//go:build windows

package runtime

import "sync"

// lingerLogf, if set, receives path-selection messages (S4U vs store URI).
// The daemon wires this to stderr. Default is a no-op.
var (
	lingerLogMu sync.Mutex
	lingerLogf  func(string, ...any)
)

// SetLingerLogf sets the optional linger path logger.
func SetLingerLogf(fn func(string, ...any)) {
	lingerLogMu.Lock()
	lingerLogf = fn
	lingerLogMu.Unlock()
}

func logLinger(format string, args ...any) {
	lingerLogMu.Lock()
	fn := lingerLogf
	lingerLogMu.Unlock()
	if fn != nil {
		fn(format, args...)
	}
}
