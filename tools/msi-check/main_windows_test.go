//go:build windows

package main

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestDirectorySecurity(t *testing.T) {
	for _, tt := range []struct {
		name, sddl string
		allowed    bool
	}{
		{"package data", "O:BAG:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)", true},
		{"package programs", "O:BAG:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;GRGX;;;BU)", true},
		{"user owner", "O:BUG:BAD:P(A;;FA;;;BA)", false},
		{"user modification", "O:BAG:BAD:P(A;;FA;;;BA)(A;;GW;;;BU)", false},
		{"inherited child modification", "O:BAG:BAD:P(A;;FA;;;BA)(A;OICIIO;GW;;;BU)", false},
		{"user child deletion", "O:BAG:BAD:P(A;;FA;;;BA)(A;;0x40;;;BU)", false},
		{"null DACL", "O:BAG:BAD:NO_ACCESS_CONTROL", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sd, err := windows.SecurityDescriptorFromString(tt.sddl)
			if err != nil {
				t.Fatal(err)
			}
			if err := protectedSecurity(sd); (err == nil) != tt.allowed {
				t.Fatalf("allowed=%v, error=%v", tt.allowed, err)
			}
		})
	}
}

func TestPreflightRejectsRelocationBeforeServiceAccess(t *testing.T) {
	if err := check([]string{"check", `C:\Apps\Other`, `C:\Data\Other`, ""}); err == nil {
		t.Fatal("relocated product directories accepted")
	}
}
