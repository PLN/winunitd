//go:build windows

package runtime

import (
	"io"
	"strings"
	"testing"
)

func TestWindowsUserDesktopHelperNames(t *testing.T) {
	valid := userDesktopPrefix + strings.Repeat("01ab", 8)
	if !validUserDesktopName(valid) {
		t.Fatal("valid generated name rejected")
	}
	for _, name := range []string{"", "WinSta0", valid + `\default`, strings.ToUpper(valid), valid + "00", userDesktopPrefix + strings.Repeat("gg", 16)} {
		if validUserDesktopName(name) {
			t.Fatalf("invalid name accepted: %q", name)
		}
	}
}

func TestWindowsUserDesktopHelperRequiresSYSTEM(t *testing.T) {
	if runningAsLocalSystem() {
		t.Skip("non-SYSTEM rejection test")
	}
	if err := ServeUserDesktopHelper("S-1-5-21-1-2-3-1001", userDesktopPrefix+strings.Repeat("01", 16), io.Discard); err == nil {
		t.Fatal("non-SYSTEM caller opened helper desktop")
	}
}
