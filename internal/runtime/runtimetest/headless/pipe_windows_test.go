//go:build windows

package headless

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func selfSID(t *testing.T) string {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return user.User.Sid.String()
}

// serveOnce answers one connection on a fresh qualification pipe.
func serveOnce(t *testing.T, allowed string, acl []string) (string, <-chan ServerEntry) {
	t.Helper()
	name := fmt.Sprintf(`%stest-%d-%d`, QualificationPipePrefix, os.Getpid(), time.Now().UnixNano())
	ln, err := ListenQualificationPipe(name, acl)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	done := make(chan ServerEntry, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			close(done)
			return
		}
		done <- serveCaller(c, allowed)
	}()
	return name, done
}

// The server holds the caller it decides about: the same account is
// accepted on its held incarnation, whatever process it is.
func TestQualificationPipeAcceptsTheAccountOnAHeldCaller(t *testing.T) {
	self := selfSID(t)
	name, done := serveOnce(t, self, []string{self})
	rep := ProbePipe(name, ClientOutsideUnit, "self", false, false)
	entry := <-done
	if !rep.Result.Accepted || rep.Result.Reason != ReasonAccepted || !rep.Result.Held {
		t.Fatalf("same account: %+v", rep.Result)
	}
	if entry.Observation.PID != windows.GetCurrentProcessId() || entry.Observation.ProcessSID != self || entry.Observation.ImpersonationSID != self {
		t.Fatalf("observation does not name this process: %+v", entry.Observation)
	}
	claim, err := selfClaim()
	if err != nil || entry.Observation.Created != claim.Created || entry.Verdict.PID != claim.PID {
		t.Fatalf("held incarnation %+v, own %+v %v", entry.Observation, claim, err)
	}
}

func TestQualificationPipeRejectsStaleClaimsAndOtherAccounts(t *testing.T) {
	self := selfSID(t)
	name, done := serveOnce(t, self, []string{self})
	if rep := ProbePipe(name, ClientStaleClaim, "stale", false, false); rep.Result.Accepted || rep.Result.Reason != ReasonClaim {
		t.Fatalf("stale claim: %+v", rep.Result)
	}
	<-done
	other := "S-1-5-21-9-9-9-4242"
	name, done = serveOnce(t, other, []string{other, self})
	if rep := ProbePipe(name, ClientWrongDecision, "none", false, false); rep.Result.Accepted || rep.Result.Reason != ReasonAccount {
		t.Fatalf("wrong account through the ACL: %+v", rep.Result)
	}
	<-done
	if self == SystemSID {
		t.Skip("the SYSTEM-only ACL admits a SYSTEM test runner")
	}
	name, _ = serveOnce(t, other, []string{other})
	if rep := ProbePipe(name, ClientWrongACL, "none", true, false); rep.Result.Connected || rep.Result.OpenError != errAccessDenied {
		t.Fatalf("wrong account at the ACL: %+v", rep.Result)
	}
}

func TestProbeOwnToken(t *testing.T) {
	p, err := ProbeOwnToken()
	if err != nil {
		t.Fatal(err)
	}
	if p.SID != selfSID(t) || p.AuthenticationID == "" || p.Integrity == "" || len(p.Groups) == 0 || len(p.Privileges) == 0 {
		t.Fatalf("token probe %+v", p)
	}
	if _, ok := p.KnownFolders["localAppData"]; !ok {
		t.Fatalf("known folders %v %v", p.KnownFolders, p.KnownFolderErrors)
	}
	if p.LogonSessionError == 0 && p.AuthPackage == "" {
		t.Fatal("logon session read without a package")
	}
}

func TestProbePathsClassifiesMissingPaths(t *testing.T) {
	dir := t.TempDir()
	cfg := &ProbeConfig{Absent: filepath.Join(dir, "missing", "file.txt"), Denied: filepath.Join(dir, "missing.txt")}
	got := ProbePaths(PathsMissingAndDenied, cfg, dir, testNonce)
	if len(got) != 2 || got[0].OK || (got[0].Win32 != errPathNotFound && got[0].Win32 != errFileNotFound) {
		t.Fatalf("absent path %+v", got)
	}
}
