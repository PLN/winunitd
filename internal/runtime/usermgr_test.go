package runtime

import (
	"errors"
	"testing"
)

func TestStartUserManagerRequiresToken(t *testing.T) {
	t.Parallel()
	_, err := StartUserManager(UserManagerSpec{
		SID: "S-1-5-21-1-2-3-1001",
		Exe: "winunitd",
	})
	if err == nil {
		t.Fatal("missing token must fail closed")
	}
	if !errors.Is(err, ErrNoUserToken) {
		t.Fatalf("err = %v, want ErrNoUserToken", err)
	}
}

func TestStartUserManagerRejectsInvalidSID(t *testing.T) {
	t.Parallel()
	_, err := StartUserManager(UserManagerSpec{SID: "not-a-sid", Exe: "winunitd"})
	if err == nil {
		t.Fatal("expected invalid SID")
	}
	_, err = StartUserManager(UserManagerSpec{SID: "S-1-5-21-1", Exe: ""})
	if err == nil {
		t.Fatal("expected missing exe")
	}
}

func TestUserManagerArgs(t *testing.T) {
	t.Parallel()
	got := UserManagerArgs("S-1-5-21-1", []string{"--base-dir", `C:\tmp`})
	if len(got) != 4 || got[0] != "--user-manager" || got[1] != "S-1-5-21-1" || got[2] != "--base-dir" {
		t.Fatalf("args = %v", got)
	}
}
