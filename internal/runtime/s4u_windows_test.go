//go:build windows

package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// This API requires no SYSTEM privilege. Exercise the real DLL binding in
// ordinary Windows CI, before the SYSTEM-only logon path can hide a panic.
func TestWindowsS4UTokenSourceIdentifier(t *testing.T) {
	first, err := newTokenSource()
	if err != nil {
		t.Fatal(err)
	}
	second, err := newTokenSource()
	if err != nil {
		t.Fatal(err)
	}
	if string(first.SourceName[:]) != "winunitd" || first.SourceIdentifier == second.SourceIdentifier {
		t.Fatalf("invalid token sources: %v, %v", first, second)
	}
}

func TestWindowsS4UTokenSourceAllocationFailure(t *testing.T) {
	failure := errors.New("allocation failed")
	source, err := newTokenSourceWithAllocator(func(id *windows.LUID) error {
		id.LowPart = 42 // A partially written native result grants no authority.
		return failure
	})
	if !errors.Is(err, failure) || source != (tokenSource{}) {
		t.Fatalf("source=%v error=%v", source, err)
	}
}

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
	if tok.Source != LingerTokenPathS4U && tok.Source != LingerTokenPathStoreURI {
		t.Fatalf("Source = %q", tok.Source)
	}
}

func TestWindowsObtainLingerTokenWithoutTCBFailsClosed(t *testing.T) {
	if runningAsLocalSystem() {
		t.Skip("non-TCB mapping; SYSTEM is TestSYSTEMLingerTokenDuplicatePrimaryAndCreateProcessAsUser")
	}
	info, err := CurrentUserInfo()
	if err != nil {
		t.Fatal(err)
	}
	tok, err := ObtainLingerToken(LingerRecord{SID: info.SID, Name: FormatAccount(info)})
	if tok != nil {
		_ = tok.Close()
		t.Fatal("non-TCB must not claim live S4U success")
	}
	if !errors.Is(err, ErrNoLingerToken) {
		t.Fatalf("err = %v, want ErrNoLingerToken", err)
	}
	low := strings.ToLower(err.Error())
	if strings.Contains(low, "password") {
		t.Fatalf("must not mention passwords: %v", err)
	}
	if !strings.Contains(err.Error(), "LsaRegisterLogonProcess") && !strings.Contains(low, "s4u") {
		t.Fatalf("non-TCB error should mention trusted LSA or S4U: %v", err)
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

func TestWindowsLogonWithSecretUsesBatchConstant(t *testing.T) {
	if logon32LogonBatch != 4 {
		t.Fatalf("URI fallback logon type = %d, want LOGON32_LOGON_BATCH (4)", logon32LogonBatch)
	}
}

// TestSYSTEMLingerTokenDuplicatePrimaryAndCreateProcessAsUser is the
// TCB-gated positive test. GitHub Actions windows-latest is not
// LocalSystem, so CI skips this. A human runs:
//
//	psexec -s -w <repo> go test ./internal/runtime -run TestSYSTEMLingerTokenDuplicatePrimaryAndCreateProcessAsUser -count=1 -v
//
// Silence (skip) is not verification.
func TestSYSTEMLingerTokenDuplicatePrimaryAndCreateProcessAsUser(t *testing.T) {
	if !runningAsLocalSystem() {
		t.Skip("requires LocalSystem (S-1-5-18 / SeTcbPrivilege); run: psexec -s -w <repo> go test ./internal/runtime -run TestSYSTEMLingerTokenDuplicatePrimaryAndCreateProcessAsUser -count=1 -v")
	}
	rec := lingerRecordForSYSTEMTest(t)
	tok, err := ObtainLingerToken(rec)
	if err != nil {
		t.Fatalf("SYSTEM S4U/fallback must produce a token: %v", err)
	}
	defer tok.Close()
	if tok.Source != LingerTokenPathS4U && tok.Source != LingerTokenPathStoreURI {
		t.Fatalf("Source = %q (want s4u or store-uri)", tok.Source)
	}
	native, ok := nativeToken(tok)
	if !ok {
		t.Fatal("linger token has no native handle")
	}
	again, err := duplicatePrimary(native)
	if err != nil {
		t.Fatalf("duplicatePrimary rejected linger token: %v", err)
	}
	_ = again.Close()

	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	ping := filepath.Join(root, "System32", "ping.exe")
	if _, err := os.Stat(ping); err != nil {
		t.Fatalf("ping.exe: %v", err)
	}
	proc, err := StartUserManager(UserManagerSpec{
		SID:     tok.Info.SID,
		Token:   tok,
		Exe:     ping,
		Env:     helperEnv(),
		cmdArgv: []string{ping, "-n", "2", "127.0.0.1"},
	})
	if err != nil {
		t.Fatalf("CreateProcessAsUser with linger token: %v", err)
	}
	defer proc.Kill()
	if !proc.Alive() {
		t.Fatal("linger-token child died immediately")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !proc.Alive() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := proc.Kill(); err != nil {
		t.Fatal(err)
	}
}

func lingerRecordForSYSTEMTest(t *testing.T) LingerRecord {
	t.Helper()
	if sid := consoleUserSID(); sid != "" && !strings.EqualFold(sid, localSystemSID) {
		info, err := LookupAccountName(sid)
		if err == nil && info.SID != "" {
			return LingerRecord{SID: info.SID, Name: FormatAccount(info)}
		}
		return LingerRecord{SID: sid}
	}
	info, err := CurrentUserInfo()
	if err != nil {
		t.Fatal(err)
	}
	return LingerRecord{SID: info.SID, Name: FormatAccount(info)}
}

func consoleUserSID() string {
	sessions, err := InteractiveSessions()
	if err != nil || len(sessions) == 0 {
		return ""
	}
	tok, err := QueryUserToken(sessions[0])
	if err != nil {
		return ""
	}
	defer tok.Close()
	return tok.Info.SID
}
