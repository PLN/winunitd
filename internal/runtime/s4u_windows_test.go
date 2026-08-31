//go:build windows

package runtime

import (
	"errors"
	"strings"
	"testing"
)

func TestWindowsObtainLingerTokenNoPassword(t *testing.T) {
	info, err := CurrentUserInfo()
	if err != nil {
		t.Fatal(err)
	}
	tok, err := ObtainLingerToken(LingerRecord{SID: info.SID, Name: FormatAccount(info)})
	if tok != nil {
		defer tok.Close()
	}
	if err != nil {
		if !errors.Is(err, ErrNoLingerToken) {
			t.Fatalf("err = %v, want ErrNoLingerToken", err)
		}
		low := strings.ToLower(err.Error())
		if strings.Contains(low, "password") {
			t.Fatalf("S4U failure must not mention passwords: %v", err)
		}
		return
	}
	if tok.Info.SID != "" && !strings.EqualFold(tok.Info.SID, info.SID) {
		t.Fatalf("SID = %q, want %q", tok.Info.SID, info.SID)
	}
}

func TestWindowsCredentialURIRejected(t *testing.T) {
	if err := ValidateCredentialURI("file://C:/pass.txt"); err == nil {
		t.Fatal("file URI must be rejected")
	}
	if err := ValidateCredentialURI("credman://winunitd/linger/user"); err != nil {
		t.Fatal(err)
	}
}
