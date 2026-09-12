//go:build windows

package runtime

import (
	"testing"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func TestInteractiveProfileUsesWindowsLogonOwnership(t *testing.T) {
	var session uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session); err != nil {
		t.Fatal(err)
	}
	if session == 0 {
		t.Skip("interactive profile test requires an interactive Windows test account")
	}
	token := testUserToken(t)
	tok, _ := nativeToken(token)
	resource, err := loadUserManagerProfile(tok, token.Info.SID)
	if err != nil {
		t.Fatal(err)
	}
	defer resource.Close()
	lease := resource.(*userProfileLease)
	if lease.interactive == 0 || lease.profile != 0 || lease.token != 0 {
		t.Fatal("interactive profile acquired a manually balanced load reference")
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal("repeated profile release failed", err)
	}
	key, err := registry.OpenKey(registry.USERS, token.Info.SID, registry.READ)
	if err != nil {
		t.Fatal("broker release disturbed the Windows-owned interactive hive", err)
	}
	if err := key.Close(); err != nil {
		t.Fatal(err)
	}
}
