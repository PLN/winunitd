//go:build windows

package headless

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
		t.Fatalf("same account: %s", rep.Result.Reason)
	}
	if entry.Observation.PID != windows.GetCurrentProcessId() || entry.Observation.ProcessSID != self || entry.Observation.ImpersonationSID != self {
		t.Fatal("the observation does not name this process and its account")
	}
	claim, err := selfClaim()
	if err != nil || entry.Observation.Created != claim.Created || entry.Verdict.PID != claim.PID || rep.Result.PID != claim.PID || rep.Result.Created != claim.Created {
		t.Fatal("the held incarnation is not this process's")
	}
}

// The server reads one request before it impersonates: a caller that sends
// a malformed request or none at all is refused for its request, and the
// server never impersonates it.
func TestQualificationPipeRefusesMissingAndMalformedRequests(t *testing.T) {
	self := selfSID(t)
	for mode, want := range map[string]string{"malformed": requestMalformed, "silent": requestMissing} {
		name, done := serveOnce(t, self, []string{self})
		rep := ProbePipe(name, ClientInUnit, mode, false, false)
		entry := <-done
		if rep.Result.Accepted || entry.Verdict.Accepted || entry.Verdict.Reason != ReasonRequest || entry.Observation.RequestError != want ||
			entry.Observation.ImpersonationSID != "" || entry.Observation.ImpersonationError != 0 {
			t.Errorf("%s request: client accepted=%t, server reason %s, request error %q, impersonated %t", mode, rep.Result.Accepted,
				entry.Verdict.Reason, entry.Observation.RequestError, entry.Observation.ImpersonationSID != "")
		}
	}
	// No claim is a well-formed request, impersonated after it is read.
	name, done := serveOnce(t, self, []string{self})
	if rep := ProbePipe(name, ClientOutsideUnit, "none", false, false); !rep.Result.Accepted {
		t.Fatalf("no claim refused: reason %s", rep.Result.Reason)
	}
	if entry := <-done; entry.Observation.ImpersonationSID != self || entry.Observation.Claim != nil {
		t.Fatal("the request without a claim was not impersonated or carried a claim")
	}
}

// A caller that exits after sending its request is held, so the server
// sees the exit and refuses it.
func TestQualificationPipeRefusesAnExitedCaller(t *testing.T) {
	self := selfSID(t)
	name, done := serveOnce(t, self, []string{self})
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe)
	// A -race build sleeps a second before it exits (atexit_sleep_ms), as long as
	// the server's settle window; this caller must exit at once.
	cmd.Env = append(os.Environ(), "GORACE="+strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"),
		helperRole+"=pipe-exit", helperPipe+"="+name)
	if err := cmd.Run(); err != nil {
		t.Fatalf("exiting client: %v", err)
	}
	entry := <-done
	if entry.Verdict.Accepted || entry.Verdict.Reason != ReasonExited || !entry.Verdict.Held || entry.Observation.PID != uint32(cmd.ProcessState.Pid()) {
		t.Fatalf("exited caller: reason %s, held %t", entry.Verdict.Reason, entry.Verdict.Held)
	}
}

func TestQualificationPipeRejectsStaleClaimsAndOtherAccounts(t *testing.T) {
	self := selfSID(t)
	name, done := serveOnce(t, self, []string{self})
	if rep := ProbePipe(name, ClientStaleClaim, "stale", false, false); rep.Result.Accepted || rep.Result.Reason != ReasonClaim {
		t.Fatalf("stale claim: accepted=%t reason %s", rep.Result.Accepted, rep.Result.Reason)
	}
	<-done
	other := "S-1-5-21-9-9-9-4242"
	name, done = serveOnce(t, other, []string{other, self})
	if rep := ProbePipe(name, ClientWrongDecision, "none", false, false); rep.Result.Accepted || rep.Result.Reason != ReasonAccount {
		t.Fatalf("wrong account through the ACL: accepted=%t reason %s", rep.Result.Accepted, rep.Result.Reason)
	}
	<-done
	if self == SystemSID {
		t.Skip("the SYSTEM-only ACL admits a SYSTEM test runner")
	}
	name, _ = serveOnce(t, other, []string{other})
	if rep := ProbePipe(name, ClientWrongACL, "none", true, false); rep.Result.Connected || rep.Result.OpenError != errAccessDenied {
		t.Fatalf("wrong account at the ACL: connected=%t error %d", rep.Result.Connected, rep.Result.OpenError)
	}
}

func TestProbeOwnToken(t *testing.T) {
	p, err := ProbeOwnToken()
	if err != nil {
		t.Fatal(err)
	}
	if p.SID != selfSID(t) || p.AuthenticationID == "" || p.Integrity == "" || len(p.Groups) == 0 || len(p.Privileges) == 0 {
		t.Fatalf("token probe: SID matches %t, logon %t, integrity %t, %d groups, %d privileges", p.SID == selfSID(t),
			p.AuthenticationID != "", p.Integrity != "", len(p.Groups), len(p.Privileges))
	}
	want := "medium"
	if p.SID == SystemSID {
		want = "system"
	} else if windows.GetCurrentProcessToken().IsElevated() {
		want = "high"
	}
	if p.Integrity != want {
		t.Fatalf("integrity %s, want %s", p.Integrity, want)
	}
	if _, ok := p.KnownFolders["localAppData"]; !ok {
		t.Fatalf("no LocalAppData: errors %v", p.KnownFolderErrors)
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
		t.Fatalf("absent path: %d results, first ok=%t error %d", len(got), len(got) > 0 && got[0].OK, func() uint32 {
			if len(got) == 0 {
				return 0
			}
			return got[0].Win32
		}())
	}
}
