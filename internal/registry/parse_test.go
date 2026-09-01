package registry

import (
	"strings"
	"testing"
)

func TestParseKey(t *testing.T) {
	t.Parallel()
	ok, err := ParseKey(`HKLM\Software\Example`)
	if err != nil {
		t.Fatal(err)
	}
	if ok.Hive != HKLM || ok.Path != `Software\Example` {
		t.Fatalf("got %+v", ok)
	}

	cu, err := ParseKey(`hkcu\Software\WinUnitd`)
	if err != nil {
		t.Fatal(err)
	}
	if cu.Hive != HKCU || cu.Path != `Software\WinUnitd` {
		t.Fatalf("got %+v", cu)
	}

	tests := []struct {
		raw     string
		wantErr string
	}{
		{"", "empty registry path"},
		{`HKLM`, `HKLM\`},
		{`HKLM\`, "empty registry path"},
		{`HKCU\`, "empty registry path"},
		{`HKLM:\Software\Example`, "PowerShell"},
		{`HKCU:\Software\Example`, "PowerShell"},
		{`HKEY_LOCAL_MACHINE\Software\Example`, `HKLM\`},
		{`HKCR\Software\Example`, `HKLM\`},
		{`HKU\Software\Example`, `HKLM\`},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			t.Parallel()
			_, err := ParseKey(tt.raw)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestKeyAllowedInScope(t *testing.T) {
	t.Parallel()
	hlm, err := ParseKey(`HKLM\Software\Example`)
	if err != nil {
		t.Fatal(err)
	}
	hcu, err := ParseKey(`HKCU\Software\Example`)
	if err != nil {
		t.Fatal(err)
	}
	if !hlm.AllowedInScope(false) || !hlm.AllowedInScope(true) {
		t.Fatal("HKLM is valid in both managers")
	}
	if hcu.AllowedInScope(false) {
		t.Fatal("HKCU is not valid in the system manager")
	}
	if !hcu.AllowedInScope(true) {
		t.Fatal("HKCU is valid in a user manager")
	}
}
