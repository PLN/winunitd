//go:build windows

package runtime

import (
	"io"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsTokenAcquisitionRetainsProtectedAuxiliary(t *testing.T) {
	var raw windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &raw); err != nil {
		t.Fatal(err)
	}
	owner := &UserToken{cleanup: []io.Closer{winToken(raw)}}
	const protectFromClose = 2
	t.Cleanup(func() {
		if len(owner.cleanup) != 0 {
			_ = windows.SetHandleInformation(windows.Handle(raw), protectFromClose, 0)
			_ = owner.Close()
		}
	})
	if err := windows.SetHandleInformation(windows.Handle(raw), protectFromClose, protectFromClose); err != nil {
		t.Fatal(err)
	}
	tok, err := finishTokenAcquisition(owner, nil)
	if err == nil || tok != owner || len(owner.cleanup) != 1 {
		t.Fatal("protected native token lost cleanup ownership")
	}
	if _, err := raw.GetTokenUser(); err != nil {
		t.Fatalf("retained token no longer valid: %v", err)
	}
	if err := windows.SetHandleInformation(windows.Handle(raw), protectFromClose, 0); err != nil {
		t.Fatal(err)
	}
	if err := tok.Close(); err != nil {
		t.Fatal(err)
	}
	if len(tok.cleanup) != 0 {
		t.Fatal("cleanup did not release unprotected native token")
	}
	if err := tok.Close(); err != nil {
		t.Fatal("released token was closed again", err)
	}
}
