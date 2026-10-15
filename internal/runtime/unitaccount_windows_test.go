//go:build windows

package runtime

import (
	"context"
	"errors"
	"fmt"
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

// The TestSYSTEMAccount tests need LocalSystem and a disposable standard
// account named by WINUNITD_TEST_ACCOUNT whose profile no other process
// holds. The test binary must be readable by that account, because the
// profile keeper runs it. Hosted CI is not LocalSystem and skips them.
func systemAccountFixture(t *testing.T) (string, *DaemonJob) {
	t.Helper()
	account := os.Getenv("WINUNITD_TEST_ACCOUNT")
	if !runningAsLocalSystem() || account == "" {
		t.Skip("requires LocalSystem and WINUNITD_TEST_ACCOUNT")
	}
	broker, err := NativeOverlapBroker()
	if err != nil {
		t.Fatal(err)
	}
	return account, broker
}

func startAs(t *testing.T, broker *DaemonJob, account, script string) *winProc {
	t.Helper()
	cmd := filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	p, err := NewLauncher(broker).Start(context.Background(), StartSpec{
		Unit: "account-test",
		Argv: []string{cmd, "/d", "/c", script},
		Dir:  os.Getenv("SystemRoot"),
		Env:  []string{"WINUNITD_ACCOUNT_TEST=from-unit"},
		User: account,
	})
	if err != nil {
		t.Fatalf("start as %s: %v", account, err)
	}
	wp := p.(*winProc)
	t.Cleanup(func() { _ = wp.Stop(5 * time.Second) })
	return wp
}

func TestSYSTEMAccountUnitRunsAsAccount(t *testing.T) {
	account, broker := systemAccountFixture(t)
	script := `whoami & echo VAR=%WINUNITD_ACCOUNT_TEST% & echo USERPROFILE=%USERPROFILE% & echo LOCALAPPDATA=%LOCALAPPDATA% & ` +
		`reg add HKCU\Software\winunitd-account-test /v probe /d hkcu-ok /f >NUL & reg query HKCU\Software\winunitd-account-test /v probe & ` +
		`echo file-ok> %LOCALAPPDATA%\winunitd-account-test.txt & type %LOCALAPPDATA%\winunitd-account-test.txt & ping -n 3 127.0.0.1 >NUL`
	p := startAs(t, broker, account, script)
	info := p.account.Info
	if !hiveLoaded(t, info.SID) {
		t.Fatal("profile is not loaded while the unit runs")
	}
	owner, session, elevated := processIdentity(t, p.PID())
	in, err := p.Job().Contains(p.PID())
	if err != nil || !in {
		t.Fatalf("process not in its unit job: %v", err)
	}
	out, _ := io.ReadAll(p.Stdout())
	errOut, _ := io.ReadAll(p.Stderr())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := p.Wait(ctx); err != nil {
		t.Fatalf("wait: %v (stderr %q)", err, errOut)
	}
	text := strings.ToLower(string(out))
	t.Logf("account %s owner=%s session=%d elevated=%t\n%s%s", FormatAccount(info), owner, session, elevated, out, errOut)
	if !strings.EqualFold(owner, info.SID) || session != 0 {
		t.Fatalf("process ran as %s in session %d, want %s in session 0", owner, session, info.SID)
	}
	short := account
	if i := strings.LastIndex(short, `\`); i >= 0 {
		short = short[i+1:]
	}
	for _, want := range []string{strings.ToLower(short), "var=from-unit", "userprofile=" + info.Profile, "localappdata=" + info.LocalAppData, "hkcu-ok", "file-ok"} {
		if !strings.Contains(text, strings.ToLower(want)) {
			t.Errorf("output lacks %q", want)
		}
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	waitHiveUnloaded(t, info.SID)
}

func TestSYSTEMAccountUnitEndsWithItsKeeper(t *testing.T) {
	account, broker := systemAccountFixture(t)
	p := startAs(t, broker, account, `ping -n 120 127.0.0.1`)
	keeper := p.account.plat.keeper.PID()
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(keeper))
	if err != nil {
		t.Fatal(err)
	}
	err = windows.TerminateProcess(h, 1)
	_ = windows.CloseHandle(h)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var exit *ExitStatus
	if err := p.Wait(ctx); !errors.As(err, &exit) || !exit.Failed() {
		t.Fatalf("unit after its keeper ended: %v", err)
	}
	if err := p.Stop(5 * time.Second); err != nil {
		t.Fatal(err)
	}
}

// ExecStop logs on separately from the process it stops. The default DACL
// Windows gives an S4U token admits the account itself, so the account's
// processes can still end each other. (taskkill.exe refuses to run under an
// S4U logon at all, so this uses TerminateProcess through PowerShell.)
func TestSYSTEMAccountProcessesCanEndEachOther(t *testing.T) {
	account, broker := systemAccountFixture(t)
	first := startAs(t, broker, account, `ping -n 120 127.0.0.1`)
	second := startAs(t, broker, account, fmt.Sprintf(`powershell.exe -NoProfile -Command "Stop-Process -Id %d -Force -ErrorAction Stop"`, first.PID()))
	out, _ := io.ReadAll(second.Stdout())
	errOut, _ := io.ReadAll(second.Stderr())
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := second.Wait(ctx); err != nil {
		t.Fatalf("taskkill as the account: %v: %s%s", err, out, errOut)
	}
	if err := first.Wait(ctx); err == nil {
		t.Fatal("the first process was not ended")
	}
}

func TestSYSTEMAccountUnitsShareTheProfile(t *testing.T) {
	account, broker := systemAccountFixture(t)
	first := startAs(t, broker, account, `ping -n 120 127.0.0.1`)
	second := startAs(t, broker, account, `ping -n 120 127.0.0.1`)
	sid := first.account.Info.SID
	if err := first.Stop(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if !hiveLoaded(t, sid) || !second.Alive() {
		t.Fatal("closing one unit's session released the other's profile")
	}
	if err := second.Stop(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	waitHiveUnloaded(t, sid)
}

func waitHiveUnloaded(t *testing.T, sid string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for hiveLoaded(t, sid) {
		if time.Now().After(deadline) {
			t.Fatal("profile still loaded a minute after the last session closed")
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

