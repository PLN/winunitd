//go:build windows

package manager

import (
	"testing"

	"github.com/PLN/winunitd/internal/notify"
)

// testNotifyListen keeps the production named pipes on Windows: each pipe name
// carries its invocation ID, so a closed pipe's name is never reused.
func testNotifyListen(t *testing.T) notify.ListenFunc {
	t.Helper()
	return nil
}
