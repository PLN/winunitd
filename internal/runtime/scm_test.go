package runtime

import "testing"

func TestServiceIdentity(t *testing.T) {
	if ServiceName != "winunitd" {
		t.Fatalf("ServiceName = %q", ServiceName)
	}
	if DisplayName != "WinUnit Manager" {
		t.Fatalf("DisplayName = %q", DisplayName)
	}
	if ServiceAccount != "LocalSystem" {
		t.Fatalf("ServiceAccount = %q", ServiceAccount)
	}
	if RecoveryActionCount != 3 {
		t.Fatalf("RecoveryActionCount = %d", RecoveryActionCount)
	}
	if RecoveryResetPeriodNever != ^uint32(0) {
		t.Fatalf("RecoveryResetPeriodNever = %d", RecoveryResetPeriodNever)
	}
	if PreshutdownTimeout <= 0 {
		t.Fatal("PreshutdownTimeout unset")
	}
}
