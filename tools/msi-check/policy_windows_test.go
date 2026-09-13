//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/runtime"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

func TestPolicyRejectsUnboundTransaction(t *testing.T) {
	for _, token := range []string{"", "../state", "a", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"} {
		if err := policy([]string{"policy-rollback", `C:\Apps`, `C:\Data`, token}); err == nil {
			t.Fatal("accepted invalid transaction identity")
		}
	}
	if err := policy([]string{"policy-unknown", `C:\Apps`, `C:\Data`, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}); err == nil {
		t.Fatal("accepted unknown policy operation")
	}
}

func TestNativeInstallerPolicyAndRollback(t *testing.T) {
	m, err := mgr.Connect()
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			t.Skip("SCM registration requires administrator access")
		}
		t.Fatal(err)
	}
	defer m.Disconnect()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("winunitd-policy-test-%d-%d", os.Getpid(), time.Now().UnixNano())
	s, err := m.CreateService(name, exe, mgr.Config{StartType: mgr.StartAutomatic, DelayedAutoStart: true})
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Skip("SCM registration requires administrator access")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	defer func() {
		if err := s.Delete(); err != nil {
			t.Errorf("fixture registration cleanup: %v", err)
		}
	}()
	// The fixture is never started and never uses the real service registration.
	original := savedPolicy{
		Exists: true, StartType: mgr.StartAutomatic, Delayed: true,
		Actions: []mgr.RecoveryAction{{Type: mgr.ServiceRestart, Delay: 7 * time.Second}, {Type: mgr.NoAction, Delay: 17 * time.Second}},
		Reset:   3600, NonCrash: false, Preshutdown: 45000,
	}
	if err := restorePolicy(s, original); err != nil {
		t.Fatal(err)
	}
	var before savedPolicy
	if err := capturePolicy(s, &before); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, original) {
		t.Fatalf("initial native policy: %+v", before)
	}
	for i := 0; i < 2; i++ {
		if err := runtime.ApplyServicePolicy(s); err != nil {
			t.Fatal(err)
		}
		var applied savedPolicy
		if err := capturePolicy(s, &applied); err != nil {
			t.Fatal(err)
		}
		if applied.StartType != mgr.StartAutomatic || applied.Delayed || applied.Reset != runtime.RecoveryResetPeriodNever || !applied.NonCrash || applied.Preshutdown != uint32(runtime.PreshutdownTimeout/time.Millisecond) || len(applied.Actions) != runtime.RecoveryActionCount {
			t.Fatalf("shared startup/recovery policy: %+v", applied)
		}
		for _, a := range applied.Actions {
			if a.Type != mgr.ServiceRestart || a.Delay != runtime.RecoveryDelay {
				t.Fatalf("recovery action: %+v", a)
			}
		}
	}
	if err := restorePolicy(s, before); err != nil {
		t.Fatal(err)
	}
	var restored savedPolicy
	if err := capturePolicy(s, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored, before) {
		t.Fatalf("rollback policy mismatch: before=%+v after=%+v", before, restored)
	}
	// The shipped beta MSI had no failure actions. The x/sys setter cannot
	// accept an empty slice; rollback must delete actions without panicking.
	empty := savedPolicy{Exists: true, StartType: mgr.StartAutomatic, Preshutdown: 180000}
	if err := restorePolicy(s, empty); err != nil {
		t.Fatal(err)
	}
	var noRecovery savedPolicy
	if err := capturePolicy(s, &noRecovery); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(noRecovery, empty) {
		t.Fatalf("no-recovery rollback mismatch: %+v", noRecovery)
	}
	state := filepath.Join(t.TempDir(), "policy.json")
	original.Version, original.Transaction = 1, "0123456789abcdef0123456789abcdef"
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	f, err := createPolicyState(state)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.Write(data)
	if err := errors.Join(writeErr, f.Sync(), f.Close()); err != nil {
		t.Fatal(err)
	}
	if duplicate, err := createPolicyState(state); err == nil {
		duplicate.Close()
		t.Fatal("existing transaction state overwritten")
	}
	loaded, err := readPolicy(state, original.Transaction)
	if err != nil || !reflect.DeepEqual(loaded, original) {
		t.Fatalf("protected state round trip: %+v, %v", loaded, err)
	}
	if _, err := readPolicy(state, "fedcba9876543210fedcba9876543210"); err == nil {
		t.Fatal("wrong transaction accepted")
	}
	if err := os.WriteFile(state, make([]byte, 64*1024+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPolicy(state, original.Transaction); err == nil {
		t.Fatal("oversized rollback state accepted")
	}
}
