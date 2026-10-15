//go:build windows

package runtime

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// TestSYSTEMAccountSessionRunsUnitAsAccount needs LocalSystem and a disposable
// standard account named by WINUNITD_TEST_ACCOUNT. The test binary must be
// readable by that account, because the profile keeper runs it. Hosted CI is
// not LocalSystem and skips it.
func TestSYSTEMAccountSessionRunsUnitAsAccount(t *testing.T) {
	account := os.Getenv("WINUNITD_TEST_ACCOUNT")
	if !runningAsLocalSystem() || account == "" {
		t.Skip("requires LocalSystem and WINUNITD_TEST_ACCOUNT")
	}
	broker, err := NativeOverlapBroker()
	if err != nil {
		t.Fatal(err)
	}
	exe, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenAccountSession(AccountSessionSpec{Account: account, Exe: exe, Daemon: broker})
	if err != nil {
		t.Fatalf("open account session: %v", err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = s.Close()
		}
	})
	sid := s.Info.SID
	if !hiveLoaded(t, sid) {
		t.Fatal("profile is not loaded while the keeper runs")
	}
	t.Logf("account %s sid=%s profile=%s", FormatAccount(s.Info), sid, s.Info.Profile)

	cmd := filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	script := `whoami & echo USERPROFILE=%USERPROFILE% & echo LOCALAPPDATA=%LOCALAPPDATA% & echo TEMP=%TEMP% & reg query HKCU\Environment /ve & ping -n 3 127.0.0.1 >NUL`
	p, err := NewLauncher(broker).Start(context.Background(), StartSpec{
		Unit: "account-test",
		Argv: []string{cmd, "/d", "/c", script},
		Dir:  os.Getenv("SystemRoot"),
		Env:  append(s.Environment(), "WINUNITD_ACCOUNT_TEST=from-unit"),
		As:   s,
	})
	if err != nil {
		t.Fatalf("start as %s: %v", account, err)
	}
	pid := p.PID()
	owner, session, elevated := processIdentity(t, pid)
	in, err := p.Job().Contains(pid)
	if err != nil || !in {
		t.Fatalf("process not in its unit job: %v", err)
	}
	out, _ := io.ReadAll(p.Stdout())
	errOut, _ := io.ReadAll(p.Stderr())
	waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := p.Wait(waitCtx); err != nil {
		t.Fatalf("wait: %v (stderr %q)", err, errOut)
	}
	_ = p.Close()
	text := string(out)
	t.Logf("owner=%s session=%d elevated=%t\n%s%s", owner, session, elevated, text, errOut)
	if !strings.EqualFold(owner, sid) || session != 0 {
		t.Fatalf("process ran as %s in session %d, want %s in session 0", owner, session, sid)
	}
	short := account
	if i := strings.LastIndex(short, `\`); i >= 0 {
		short = short[i+1:]
	}
	for _, want := range []string{
		strings.ToLower(short),
		"USERPROFILE=" + s.Info.Profile,
		"LOCALAPPDATA=" + s.Info.LocalAppData,
		`HKEY_CURRENT_USER\Environment`,
	} {
		if !strings.Contains(strings.ToLower(text), strings.ToLower(want)) {
			t.Errorf("output lacks %q", want)
		}
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	closed = true
	deadline := time.Now().Add(60 * time.Second)
	for hiveLoaded(t, sid) {
		if time.Now().After(deadline) {
			t.Fatal("profile still loaded a minute after the session closed")
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func hiveLoaded(t *testing.T, sid string) bool {
	t.Helper()
	key, err := registry.OpenKey(registry.USERS, sid, registry.READ)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	_ = key.Close()
	return true
}

func processIdentity(t *testing.T, pid int) (sid string, session uint32, elevated bool) {
	t.Helper()
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	var tok windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok); err != nil {
		t.Fatal(err)
	}
	defer tok.Close()
	user, err := tok.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	var returned uint32
	if err := windows.GetTokenInformation(tok, windows.TokenSessionId, (*byte)(unsafe.Pointer(&session)), uint32(unsafe.Sizeof(session)), &returned); err != nil {
		t.Fatal(err)
	}
	return user.User.Sid.String(), session, tok.IsElevated()
}
