//go:build windows

package runtime

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// sessionAccountFixture is systemAccountFixture for an account that is signed
// in to a session: the TestSYSTEMSessionLogon tests skip when it is not.
func sessionAccountFixture(t *testing.T) (string, *DaemonJob, string) {
	t.Helper()
	account, broker := systemAccountFixture(t)
	info, err := LookupAccountName(account)
	if err != nil {
		t.Fatal(err)
	}
	if !nativeSessionLogon.signedInAnywhere(info.SID) {
		t.Skip("requires WINUNITD_TEST_ACCOUNT signed in to a session")
	}
	return account, broker, info.SID
}

func (api sessionLogonAPI) signedInAnywhere(sid string) bool {
	sessions, err := api.sessions()
	if err != nil {
		return false
	}
	for _, s := range sessions {
		t, err := api.token(s.ID)
		if err != nil {
			continue
		}
		got, err := api.userSID(t)
		_ = t.Close()
		if err == nil && strings.EqualFold(got, sid) {
			return true
		}
	}
	return false
}

func startWithSessionLogon(t *testing.T, broker *DaemonJob, account string) *winProc {
	t.Helper()
	cmd := os.Getenv("SystemRoot") + `\System32\cmd.exe`
	p, err := NewLauncher(broker).Start(context.Background(), StartSpec{
		Unit:        "session-logon-test",
		Argv:        []string{cmd, "/d", "/c", "whoami & ping -n 2 127.0.0.1 >NUL"},
		Dir:         os.Getenv("SystemRoot"),
		User:        account,
		UserSession: true,
	})
	if err != nil {
		t.Fatalf("start as %s: %v", account, err)
	}
	wp := p.(*winProc)
	t.Cleanup(func() { _ = wp.Stop(5 * time.Second) })
	return wp
}

func TestSYSTEMSessionLogonRunsInSessionZero(t *testing.T) {
	account, broker, sid := sessionAccountFixture(t)
	p := startWithSessionLogon(t, broker, account)
	logon, session := p.AccountLogon()
	if logon != LogonSession || session == 0 {
		t.Fatalf("logon %q session %d, want a session logon", logon, session)
	}
	owner, processSession, _ := processIdentity(t, p.PID())
	in, err := p.Job().Contains(p.PID())
	if err != nil || !in {
		t.Fatalf("process not in its unit job: %v", err)
	}
	if !strings.EqualFold(owner, sid) || processSession != 0 {
		t.Fatalf("process ran as %s in session %d, want %s in session 0", owner, processSession, sid)
	}
	out, _ := io.ReadAll(p.Stdout())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := p.Wait(ctx); err != nil {
		t.Fatalf("wait: %v", err)
	}
	t.Logf("session %d: %s", session, out)
}

// A failure to obtain the full token is a failure of the session path: the
// same open continues to S4U, and the filtered token is never duplicated.
func TestSYSTEMSessionLogonLinkedFailureFallsBackToS4U(t *testing.T) {
	account, broker, _ := sessionAccountFixture(t)
	saved := nativeSessionLogon
	defer func() { nativeSessionLogon = saved }()
	duplicated := 0
	nativeSessionLogon.filtered = func(io.Closer) (bool, error) { return true, nil }
	nativeSessionLogon.linked = func(io.Closer) (io.Closer, error) { return nil, errors.New("injected linked token failure") }
	nativeSessionLogon.duplicate = func(c io.Closer) (io.Closer, error) {
		duplicated++
		return saved.duplicate(c)
	}
	p := startWithSessionLogon(t, broker, account)
	if logon, _ := p.AccountLogon(); logon != LogonS4U || duplicated != 0 {
		t.Fatalf("logon %q after %d session duplicates, want S4U and none", logon, duplicated)
	}
}
