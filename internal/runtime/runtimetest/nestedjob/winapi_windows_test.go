//go:build windows

package nestedjob

import (
	"errors"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A failed or short elevation read is an error of the identity, never a
// non-elevated token.
func TestElevationReadFailureIsAnError(t *testing.T) {
	answer := func(value, size uint32, err error) func(*byte, uint32, *uint32) error {
		return func(buf *byte, _ uint32, n *uint32) error {
			if err != nil {
				return err
			}
			*(*uint32)(unsafe.Pointer(buf)) = value
			*n = size
			return nil
		}
	}
	if _, err := readElevation(answer(0, 0, windows.ERROR_ACCESS_DENIED)); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("failed query: %v", err)
	}
	if _, err := readElevation(answer(0, 2, nil)); err == nil {
		t.Fatal("a short read was accepted")
	}
	for value, want := range map[uint32]bool{0: false, 1: true} {
		if got, err := readElevation(answer(value, 4, nil)); err != nil || got != want {
			t.Fatalf("elevation %d: %v %v", value, got, err)
		}
	}
	if _, err := tokenElevated(windows.Token(0)); err == nil {
		t.Fatal("the elevation of no token was reported")
	}
	tok := windows.GetCurrentProcessToken()
	if got, err := tokenElevated(tok); err != nil || got != tok.IsElevated() {
		t.Fatalf("own token: %v %v", got, err)
	}
}
