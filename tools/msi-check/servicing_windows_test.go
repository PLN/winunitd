//go:build windows

package main

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

func TestServiceRollbackWithoutPrepareDoesNotInspectRegistration(t *testing.T) {
	// Invalid directories would fail a real prepare. With no saved state,
	// rollback has nothing to undo and does not open SCM.
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(nonce[:])
	if err := serviceTransaction([]string{"service-rollback", `C:\Apps\Other`, `C:\Data\Other`, token}); err != nil {
		t.Fatalf("rollback without prepare: %v", err)
	}
}

func TestServiceTransactionRejectsUnboundToken(t *testing.T) {
	for _, token := range []string{"", "../state", "a", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"} {
		if err := serviceTransaction([]string{"service-rollback", `C:\Apps`, `C:\Data`, token}); err == nil {
			t.Fatal("accepted invalid transaction identity")
		}
	}
	if err := serviceTransaction([]string{"service-unknown", `C:\Apps`, `C:\Data`, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}); err == nil {
		t.Fatal("accepted unknown service operation")
	}
}

func TestProductServiceIdentity(t *testing.T) {
	install := `C:\Program Files\winunitd`
	data := `C:\ProgramData\winunitd`
	ok := mgr.Config{
		ServiceStartName: "LocalSystem",
		BinaryPathName:   `"C:\Program Files\winunitd\bin\winunitd.exe" --base-dir "C:\ProgramData\winunitd\."`,
	}
	if err := productServiceIdentity(ok, install, data); err != nil {
		t.Fatal(err)
	}
	authority := ok
	authority.ServiceStartName = `NT AUTHORITY\SYSTEM`
	if err := productServiceIdentity(authority, install, data); err != nil {
		t.Fatal(err)
	}
	betaLayout := ok
	betaLayout.BinaryPathName = `"C:\Program Files\winunitd\winunitd.exe" --base-dir "C:\ProgramData\winunitd\."`
	if err := productServiceIdentity(betaLayout, install, data); err == nil {
		t.Fatal("accepted a service image outside bin")
	}
	otherAccount := ok
	otherAccount.ServiceStartName = "NT AUTHORITY\\LocalService"
	if err := productServiceIdentity(otherAccount, install, data); err == nil {
		t.Fatal("accepted a non-LocalSystem service")
	}
}

func TestPayloadShareNoneDetectsLiveHandle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "winunitd.exe")
	if err := os.WriteFile(path, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	inUse, err := payloadInUse(path)
	if err != nil || inUse {
		t.Fatalf("idle file: inUse=%v err=%v", inUse, err)
	}
	missing, err := payloadInUse(filepath.Join(dir, "absent.exe"))
	if err != nil || missing {
		t.Fatalf("missing file: inUse=%v err=%v", missing, err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	inUse, err = payloadInUse(path)
	if err != nil || !inUse {
		t.Fatalf("held file: inUse=%v err=%v", inUse, err)
	}
	if _, err := payloadInUse(dir); err == nil {
		t.Fatal("directory accepted as a payload file")
	}
}
