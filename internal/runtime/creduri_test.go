package runtime

import (
	"errors"
	"strings"
	"testing"
)

func TestParseCredentialURI(t *testing.T) {
	t.Parallel()
	ok, err := ParseCredentialURI("credman://winunitd/linger/S-1-5-21-1")
	if err != nil {
		t.Fatal(err)
	}
	if ok != "credman://winunitd/linger/S-1-5-21-1" {
		t.Fatalf("got %q", ok)
	}
	ok, err = ParseCredentialURI("lsa://winunitd/linger/user")
	if err != nil || ok != "lsa://winunitd/linger/user" {
		t.Fatalf("lsa = %q err=%v", ok, err)
	}
	if _, err := ParseCredentialURI(""); err != nil {
		t.Fatal(err)
	}
}

func TestParseCredentialURIRejectsSecrets(t *testing.T) {
	t.Parallel()
	bads := []string{
		"credman://x?password=secret",
		"file://C:/secrets/pass.txt",
		"env:PASSWORD",
		"password=hunter2",
		"http://example",
		"credman://user:secret@host/name",
		"credman://",
	}
	for _, raw := range bads {
		if _, err := ParseCredentialURI(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func TestObtainLingerTokenFailsClosedOffWindows(t *testing.T) {
	t.Parallel()
	_, err := ObtainLingerToken(LingerRecord{SID: "S-1-5-21-1-2-3-1001", Name: "user"})
	if err == nil {
		// Windows with TCB may succeed; still must not mention passwords.
		return
	}
	if !errors.Is(err, ErrNoLingerToken) && !strings.Contains(strings.ToLower(err.Error()), "s4u") && !strings.Contains(err.Error(), "Windows") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "password") {
		t.Fatalf("error must not mention passwords: %v", err)
	}
}

func TestFormatAccount(t *testing.T) {
	t.Parallel()
	got := FormatAccount(UserInfo{Username: "alice", Domain: "TEST"})
	if got != `TEST\alice` {
		t.Fatalf("got %q", got)
	}
}

func TestLookupAccountNameSIDOnLinux(t *testing.T) {
	t.Parallel()
	info, err := LookupAccountName("S-1-5-21-1-2-3-1001")
	if err != nil {
		t.Fatal(err)
	}
	if info.SID != "S-1-5-21-1-2-3-1001" {
		t.Fatalf("SID = %q", info.SID)
	}
}
