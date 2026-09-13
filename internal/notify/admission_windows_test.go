//go:build windows

package notify

import (
	"os"
	"testing"

	"github.com/PLN/winunitd/internal/protocol"
)

func TestWindowsNotificationConnectionAdmission(t *testing.T) {
	sid, err := protocol.CurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	lis, err := Listen("bounded-notify.service", sid)
	if err != nil {
		t.Fatal(err)
	}
	checkNotificationAdmission(t, lis, os.Getpid())
}
