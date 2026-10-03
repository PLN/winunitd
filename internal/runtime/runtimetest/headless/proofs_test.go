package headless

import (
	"slices"
	"strings"
	"testing"
)

// mutationCase changes a complete record set and names the problem it must
// cause.
type mutationCase struct {
	name, want string
	mutate     func([]Record)
}

func runMutations(t *testing.T, cases []mutationCase) {
	t.Helper()
	m := testMatrix(t)
	run := testRun(t)
	base := allPassing(t, m)
	for _, tc := range cases {
		rs := cloneRecords(base)
		tc.mutate(rs)
		s := Summarize(m, rs, run, Selection{})
		if s.Complete || onlyPending(m, s) || !strings.Contains(strings.Join(s.Problems, "\n"), tc.want) {
			t.Errorf("%s: problems %q", tc.name, s.Problems)
		}
	}
}

func ev(t *testing.T, rs []Record, key string) *Evidence {
	t.Helper()
	return &rs[findRecord(t, rs, key)].Evidence
}

// Each case-specific proof is required, and another case's or a wrong one
// does not stand in for it.
func TestSummarizeRequiresCaseProofs(t *testing.T) {
	runMutations(t, []mutationCase{
		// First use.
		{"no first-use proof", "H01/A: control first-use no first-use proof", func(rs []Record) { ev(t, rs, "H01/A#first-use").FirstUse = nil }},
		{"first use of another account", "the check names another account", func(rs []Record) { ev(t, rs, "H01/A#first-use").FirstUse.SID = sidB }},
		{"profile already registered", "the account was already used", func(rs []Record) { ev(t, rs, "H01/A#first-use").FirstUse.ProfileList = true }},
		{"logon before the boot", "the account was already used", func(rs []Record) { ev(t, rs, "H01/A#first-use").FirstUse.LogonSessions = 1 }},
		{"failed first-use query", "a first-use query failed", func(rs []Record) {
			ev(t, rs, "H01/A#first-use").FirstUse.Errors = []NativeError{{Op: "hive", Win32: 5}}
		}},
		{"no baseline", "the check names no baseline", func(rs []Record) { ev(t, rs, "H01/A#first-use").FirstUse.Baseline = "" }},
		{"check relabelled with the cold boot", "H01/A: control first-use is recorded on another boot than it ran on", func(rs []Record) {
			rs[findRecord(t, rs, "H01/A#first-use")].BootID = testBoot(1).String()
		}},
		{"check not on the preceding boot", "the observed cold boot is not the boot after the check", func(rs []Record) {
			ev(t, rs, "H01/A#first-use").FirstUse.Boot.Counter = 5
		}},
		// Cold boot.
		{"no cold-boot progress", "H01/A: progressing below its minimum", func(rs []Record) {
			for _, k := range []string{"H01/A", "H02/B"} {
				delete(ev(t, rs, k).Observer.Progress, AccountA)
			}
		}},
		{"progress of another incarnation", "H02/B: progressing below its minimum", func(rs []Record) {
			for _, k := range []string{"H01/A", "H02/B"} {
				p := ev(t, rs, k).Observer.Progress[AccountB]
				p.Created++
				ev(t, rs, k).Observer.Progress[AccountB] = p
			}
		}},
		{"first-use profile existed", "H01/A: profileCreated below its minimum", func(rs []Record) {
			for _, k := range []string{"H01/A", "H02/B"} {
				p := ev(t, rs, k).Observer.Profiles[AccountA]
				p.DirectoryCreated = ft(-90000)
				ev(t, rs, k).Observer.Profiles[AccountA] = p
			}
		}},
		{"existing profile recreated", "H02/B: profileExisting below its minimum", func(rs []Record) {
			for _, k := range []string{"H01/A", "H02/B"} {
				p := ev(t, rs, k).Observer.Profiles[AccountB]
				p.DirectoryCreated = ft(-10)
				ev(t, rs, k).Observer.Profiles[AccountB] = p
			}
		}},
		{"interactive session at the cold boot", "H01/A: interactiveSessions above its maximum", func(rs []Record) {
			for _, k := range []string{"H01/A", "H02/B"} {
				r := ev(t, rs, k).Observer
				r.Sessions = append(r.Sessions, SessionSample{At: ft(1), Users: []SessionUser{{Session: 1, SID: sidAdmin}}})
			}
		}},
		{"no S4U manager on the boot", "H02/B: bootStarted below its minimum", func(rs []Record) {
			for _, k := range []string{"H01/A", "H02/B"} {
				gensOf(ev(t, rs, k).Observer, RoleManager, AccountB)[0].Created = ft(-4000)
			}
		}},
		// Session and revocation.
		{"no session cycle", "H04/A: sessionCycle below its minimum", func(rs []Record) {
			r := ev(t, rs, "H04/A").Observer
			r.Sessions = r.Sessions[:1]
		}},
		{"headless manager replaced at logon", "H04/B: kept below its minimum", func(rs []Record) {
			r := ev(t, rs, "H04/B").Observer
			for _, g := range gensOf(r, RoleManager, AccountB) {
				if g.Token.Session == 0 {
					g.Exited = ft(12)
				}
			}
		}},
		{"hive still loaded", "H05/A: profileUnloaded below its minimum", func(rs []Record) {
			r := ev(t, rs, "H05/A").Observer
			p := r.Profiles[AccountA]
			p.HiveLoaded = true
			r.Profiles[AccountA] = p
		}},
		// Named tests.
		{"no test receipt", "H20/revocation/r1: no test receipt", func(rs []Record) { ev(t, rs, "H20/revocation/r1").TestRun = nil }},
		{"another admitted binary", "the test binary is not the admitted runtime.test.exe", func(rs []Record) {
			tr := ev(t, rs, "H20/shutdown/r2").TestRun
			tr.Artifact, tr.SHA256 = "other.exe", testOtherSHA
		}},
		{"right name, other hash", "the test binary is not the admitted runtime.test.exe", func(rs []Record) {
			ev(t, rs, "H20/shutdown/r3").TestRun.SHA256 = testOtherSHA
		}},
		{"skipped subtest", "the test or a subtest was skipped", func(rs []Record) {
			tr := ev(t, rs, "H21/security-A").TestRun
			tr.Events[2].Action = "skip"
		}},
		{"another test's receipt", "the receipt covers another test", func(rs []Record) {
			*ev(t, rs, "H21/security-B").TestRun = *ev(t, rs, "H21/sensitivity-B").TestRun
		}},
		{"test not run", "the named test did not run once and pass", func(rs []Record) {
			tr := ev(t, rs, "H20/deadline/r4").TestRun
			tr.Events = tr.Events[1:]
		}},
		{"runner not SYSTEM", "the runner is not SYSTEM in session zero", func(rs []Record) {
			ev(t, rs, "H20/deadline/r5").TestRun.Runner.Token.SID = sidA
		}},
		{"receipt of another runner", "the receipt names another runner", func(rs []Record) {
			ev(t, rs, "H20/revocation/r2").TestRun.Runner.ID = "runner-r9"
		}},
		{"no S4U subject", "the test reported no S4U subject", func(rs []Record) { ev(t, rs, "H21/security-A").TestRun.Subject = nil }},
		{"subject under a session token", "the test's subject is not the account's genuine S4U token", func(rs []Record) {
			s := tokenFacts(AccountB, ModeWTS)
			ev(t, rs, "H21/security-B").TestRun.Subject = &s
		}},
		// Status snapshots.
		{"not unlimited", "G5/A: unlimited below its minimum", func(rs []Record) { ev(t, rs, "G5/A").Status.Budget.Burst = 5 }},
		{"no status", "G5/B: unlimited not supported by the evidence", func(rs []Record) { ev(t, rs, "G5/B").Status = nil }},
		{"finite control past its burst", "G5/A: control finite-limit withinBurst below its minimum", func(rs []Record) {
			r := ev(t, rs, "G5/A#finite-limit").Observer
			o := &observed{ObserverReport: r, pid: 9000}
			o.gen(RoleWorkload, AccountA, ModeS4U, 9, 9.5, 7)
		}},
		{"finite control without status", "G5/B: control finite-limit startLimited not supported by the evidence", func(rs []Record) {
			ev(t, rs, "G5/B#finite-limit").Status = nil
		}},
		// Endpoints.
		{"no endpoint identities", "H16/A: control pipe-health endpoint system-only missing", func(rs []Record) { ev(t, rs, "H16/A#pipe-health").Endpoints = nil }},
		{"endpoint restarted", "endpoint control-pipe was not the same live server before and after", func(rs []Record) {
			ev(t, rs, "H16/B#pipe-health").Endpoints[2].After.PID++
		}},
		{"control pipe served by another program", "endpoint control-pipe is not served by its genuine server", func(rs []Record) {
			e := &ev(t, rs, "H16/A#pipe-health").Endpoints[2]
			e.Before.Image, e.After.Image = workloadImage, workloadImage
		}},
		{"peer pipe served by SYSTEM", "endpoint peer-user-pipe is not served by its genuine server", func(rs []Record) {
			e := &ev(t, rs, "H16/B#pipe-health").Endpoints[1]
			e.Before.SID, e.After.SID = SystemSID, SystemSID
		}},
		{"denial from outside the unit", "H16/A: pipe control-pipe was not a process of the account's unit", func(rs []Record) {
			ev(t, rs, "H16/A").Pipe[2].PID = 1
		}},
		// Password-bearing controls.
		{"password control under S4U", "H18/A: control password-share is not the account's password-bearing probe", func(rs []Record) {
			*ev(t, rs, "H18/A#password-share").Token = *tokenProbeOf(Generation{PID: 1, Created: 1, Token: tokenFacts(AccountA, ModeS4U)})
			rs[findRecord(t, rs, "H18/A#password-share")].Token.Session = 0
		}},
		{"password control of another account", "H19/B#password-decrypt: account B ran under another SID than its other records", func(rs []Record) {
			ev(t, rs, "H19/B#password-decrypt").Token.SID = sidA
			rs[findRecord(t, rs, "H19/B#password-decrypt")].Token.SID = sidA
		}},
		{"read-only share", "H18/A: characterization inconclusive: the same account's password logon did not write beside the file either", func(rs []Record) {
			smb := ev(t, rs, "H18/A").SMB
			smb.Read = OpResult{Op: "read", OK: true, SHA256: testContent}
			smb.Write = OpResult{Op: "write", Win32: errAccessDenied}
			ev(t, rs, "H18/A#password-share").SMB.Write = OpResult{Op: "write", Win32: errAccessDenied}
		}},
		{"password control on another file", "H19/B: characterization inconclusive: the password logon decrypted another file", func(rs []Record) {
			ev(t, rs, "H19/B#password-decrypt").EFS.Target = `C:\other.txt`
		}},
		// Named roles.
		{"recorded by another admitted program", "H13/A: not made by the admitted headless-workload.exe", func(rs []Record) {
			rs[findRecord(t, rs, "H13/A")].Executable = testOtherSHA
		}},
		// No password-bearing logon in the lifecycle phase.
		{"password logon in the lifecycle phase", "H07/A: phase lifecycle ran after a password-bearing logon", func(rs []Record) {
			r := ev(t, rs, "H07/A").Observer
			r.Logons = []LogonFact{{ID: "1", SID: sidA, Type: logonInteractive, LogonTime: ft(1), Source: "audit", Process: "User32"}}
		}},
	})
}

// The reviewer's contradictory observations: none of them leaves a case
// accepted.
func TestSummarizeRefusesContradictoryEvidence(t *testing.T) {
	runMutations(t, []mutationCase{
		{"crash loop without real processes", "G2/A: observer: generation", func(rs []Record) {
			for i := range ev(t, rs, "G2/A").Observer.Generations {
				g := &ev(t, rs, "G2/A").Observer.Generations[i]
				if g.Account == AccountA {
					g.PID, g.Created = 0, 0
				}
			}
		}},
		{"impossible old tree", "H07/A: observer: generation", func(rs []Record) {
			g := gensOf(ev(t, rs, "H07/A").Observer, RoleWorkload, AccountA)[0]
			g.PID, g.Created = 0, 0
		}},
		{"launch at the start of the negative window", "G4/linger-A: negativeSec below its minimum", func(rs []Record) {
			r := ev(t, rs, "G4/linger-A").Observer
			r.Generations = append(r.Generations, Generation{Role: RoleManager, Account: AccountA, PID: 9, Created: r.Marks[0].At,
				Seen: r.Marks[0].At + 5e5, Token: tokenFacts(AccountA, ModeS4U)})
		}},
		{"token probe of elevated SYSTEM in session 7", "H13/A: the record's token context disagrees with its token probe", func(rs []Record) {
			tp := ev(t, rs, "H13/A").Token
			tp.SID, tp.Session, tp.Elevated = SystemSID, 7, true
		}},
		{"token probe of another process", "H17/B: the probing process is not a held process of the account's unit", func(rs []Record) {
			ev(t, rs, "H17/B").Token.PID = 4242
		}},
		{"token probe disagrees with the held token", "H18/A: the token probe disagrees with the held process's token", func(rs []Record) {
			ev(t, rs, "H18/A").Token.AuthenticationID = "00000000:00099999"
		}},
		{"pipe verdicts without server", "H15/A: pipe no SYSTEM qualification server report", func(rs []Record) { ev(t, rs, "H15/A").PipeServers = nil }},
		{"server verdict contradicts its observation", "H15/B: pipe a server verdict does not follow from its observation", func(rs []Record) {
			en := &ev(t, rs, "H15/B").PipeServers[0].Entries[1]
			en.Observation.ProcessSID = sidA
		}},
		{"qualification server not SYSTEM", "H15/A: pipe a qualification server was not SYSTEM in session zero", func(rs []Record) {
			ev(t, rs, "H15/A").PipeServers[0].Server.SID = sidA
		}},
		{"in-unit client outside the unit", "H15/A: pipe the in-unit client was not a process of the account's unit", func(rs []Record) {
			e := ev(t, rs, "H15/A")
			e.Pipe[0].PID, e.Pipe[0].Created = e.Pipe[1].PID, e.Pipe[1].Created
			e.PipeServers[0].Entries[0] = e.PipeServers[0].Entries[1]
		}},
		{"client reports a verdict the server did not give", "H15/B: pipe outside-unit received another verdict than the server recorded", func(rs []Record) {
			ev(t, rs, "H15/B").Pipe[1].Reason = ReasonClaim
		}},
		{"no exited caller", "H15/B: pipe no held caller of the account was refused for exiting", func(rs []Record) {
			en := ev(t, rs, "H15/B").PipeServers[0].Entries
			ev(t, rs, "H15/B").PipeServers[0].Entries = en[:3]
		}},
		{"wrong account through the default ACL", "H15/A: pipe the wrong-account decision was not made through an ACL that admitted it", func(rs []Record) {
			ev(t, rs, "H15/A").PipeServers[1].ACL = []string{sidA, sidAdmin}
		}},
	})
}

func TestFilteredAdministrator(t *testing.T) {
	filtered := &TokenProbe{ElevationType: tokenElevationTypeLimit, Groups: []TokenGroup{{SID: builtinAdministrators, Attributes: groupUseForDenyOnly}}}
	if !FilteredAdministrator(filtered) {
		t.Fatal("a filtered administrator token rejected")
	}
	for name, tp := range map[string]*TokenProbe{
		"ordinary medium token":  {ElevationType: 1},
		"elevated":               {ElevationType: 2, Elevated: true, Groups: filtered.Groups},
		"enabled Administrators": {ElevationType: tokenElevationTypeLimit, Groups: []TokenGroup{{SID: builtinAdministrators, Attributes: 7}}},
		"no probe":               nil,
	} {
		if FilteredAdministrator(tp) {
			t.Errorf("%s accepted as a filtered administrator", name)
		}
	}
}

func TestParseTestEvents(t *testing.T) {
	out := `{"Time":"2026-10-03T12:00:00Z","Action":"start","Package":"p"}
{"Action":"run","Package":"p","Test":"TestA"}
{"Action":"output","Package":"p","Test":"TestA","Output":"=== RUN TestA\n"}
{"Action":"run","Package":"p","Test":"TestA/sub"}
{"Action":"skip","Package":"p","Test":"TestA/sub","Elapsed":0}
{"Action":"pass","Package":"p","Test":"TestA","Elapsed":1.5}
{"Action":"pass","Package":"p","Elapsed":2}
`
	events, err := ParseTestEvents(strings.NewReader(out))
	want := []TestEvent{{"run", "TestA"}, {"run", "TestA/sub"}, {"skip", "TestA/sub"}, {"pass", "TestA"}}
	if err != nil || !slices.Equal(events, want) {
		t.Fatalf("events %v %v", events, err)
	}
	if _, err := ParseTestEvents(strings.NewReader("--- PASS: TestA\n")); err == nil {
		t.Fatal("plain test output accepted")
	}
}

func TestAdmissionLookup(t *testing.T) {
	run := testRun(t)
	if run.Manifest.Lookup(workloadImage) != testExecutable() || run.Manifest.Lookup("runtime.test.exe") != testRuntimeSHA || run.Manifest.Lookup("missing.exe") != "" {
		t.Fatal("lookup by name")
	}
	var none *Admission
	if none.Lookup(workloadImage) != "" {
		t.Fatal("lookup without a manifest")
	}
}

// The diagnostics, independent-permission and final-cleanup proofs.
func TestSummarizeRequiresDiagnosticsProofs(t *testing.T) {
	gen := func(r *ObserverReport, role, account string, i int) *Generation { return gensOf(r, role, account)[i] }
	runMutations(t, []mutationCase{
		// Rotation (H12 and the session lanes).
		{"no daemon-log proof", "H12/A: no daemon-log proof", func(rs []Record) { ev(t, rs, "H12/A").DaemonLog = nil }},
		{"log not padded", "H12/B: the stopped log was not padded past the rotation size", func(rs []Record) {
			ev(t, rs, "H12/B").DaemonLog.Before.Current.Size = RotationBytes - 1
		}},
		{"padded log not rotated", "H12/A: the padded log was not rotated to the archive unchanged", func(rs []Record) {
			ev(t, rs, "H12/A").DaemonLog.After.Archive.SHA256 = strings.Repeat("1", 64)
		}},
		{"no fresh log", "G6/standard-wts: no fresh current log after the rotation", func(rs []Record) {
			ev(t, rs, "G6/standard-wts").DaemonLog.After.Current.Size = RotationBytes
		}},
		{"archive readable by others", "G6/filtered-admin: the archive is not protected for its manager only", func(rs []Record) {
			a := ev(t, rs, "G6/filtered-admin").DaemonLog.After.Archive
			a.DACL += "(A;;FR;;;BU)"
		}},
		{"unprotected directory", "H12/B: the daemon directory is not protected for its manager only", func(rs []Record) {
			d := ev(t, rs, "H12/B").DaemonLog.After.Dir
			d.DACL = strings.Replace(d.DACL, "D:P", "D:", 1)
		}},
		{"current log owned by another account", "H12/A: the current log has an unexpected owner", func(rs []Record) {
			ev(t, rs, "H12/A").DaemonLog.After.Current.Owner = sidB
		}},
		{"no open record after the padding", "G6/standard-wts: no daemon.open record after the intervention", func(rs []Record) {
			ev(t, rs, "G6/standard-wts").DaemonLog.After.Tail[0].At = ft(5)
		}},
		{"manager not restarted", "H12/A: the observer held no manager of the account started after the intervention, before its open record and still running", func(rs []Record) {
			gen(ev(t, rs, "H12/A").Observer, RoleManager, AccountA, 0).Created = ft(5)
		}},
		{"session lane under an elevated token", "G6/filtered-admin: the observer held no manager of the account started after the intervention", func(rs []Record) {
			gen(ev(t, rs, "G6/filtered-admin").Observer, RoleManager, AccountAdmin, 0).Token.ElevationType = 1
		}},
		{"proof of another account", "H12/B: the daemon-log proof names another account", func(rs []Record) { ev(t, rs, "H12/B").DaemonLog.SID = sidA }},
		{"failed daemon-log query", "H12/A: a daemon-log query failed", func(rs []Record) {
			ev(t, rs, "H12/A").DaemonLog.After.Errors = []NativeError{{Op: "archive", Win32: 5}}
		}},
		// Coherent, bound diagnostics.
		{"no time on the final facts", "H12/A: the daemon-log facts have no time or root", func(rs []Record) { ev(t, rs, "H12/A").DaemonLog.After.At = 0 }},
		{"open record after the final read", "H12/B: no daemon.open record after the intervention", func(rs []Record) {
			ev(t, rs, "H12/B").DaemonLog.After.Tail[0].At = ft(500)
		}},
		{"open record beyond the observation", "H12/A: the open record is outside the observation", func(rs []Record) {
			d := ev(t, rs, "H12/A").DaemonLog
			d.After.At, d.After.Tail[0].At = ft(5000), ft(4000)
		}},
		{"manager started after its open record", "G6/standard-wts: the observer held no manager of the account started after the intervention, before its open record", func(rs []Record) {
			ev(t, rs, "G6/standard-wts").DaemonLog.After.Tail[0].At = ft(12)
		}},
		{"negative log size", "H12/B: a log file has an incoherent size or hash", func(rs []Record) { ev(t, rs, "H12/B").DaemonLog.After.Current.Size = -1 }},
		{"no archive hash", "G6/filtered-admin: a log file has an incoherent size or hash", func(rs []Record) {
			ev(t, rs, "G6/filtered-admin").DaemonLog.After.Archive.SHA256 = ""
		}},
		{"log padded under a running manager", "H12/A: the account's manager was running when the log was prepared", func(rs []Record) {
			ev(t, rs, "H12/A").DaemonLog.Before.ManagersRunning = 1
		}},
		{"facts read under another root", "H12/B: the daemon-log facts are not the account's manager's data root", func(rs []Record) {
			ev(t, rs, "H12/B").DaemonLog.Root = `C:\Users\other\AppData\Local\winunitd`
		}},
		{"no measured profile", "G6/legacy-repair: the daemon-log facts are not the account's manager's data root", func(rs []Record) {
			delete(ev(t, rs, "G6/legacy-repair").Observer.Profiles, AccountA)
		}},
		{"system facts under a user root", "G6/system-protection: the daemon-log facts are not the system manager's data root", func(rs []Record) {
			ev(t, rs, "G6/system-protection").DaemonLog.Root = `C:\Users\wu-a\AppData\Local\winunitd`
		}},
		{"legacy directory denying everyone", "G6/legacy-repair: the directory was not the declared legacy", func(rs []Record) {
			ev(t, rs, "G6/legacy-repair").DaemonLog.Before.Dir.DACL = "D:(D;OICI;FA;;;WD)"
		}},
		{"legacy directory not openly writable", "G6/legacy-repair: the directory was not the declared legacy", func(rs []Record) {
			ev(t, rs, "G6/legacy-repair").DaemonLog.Before.Dir.DACL = "D:AI(A;OICI;FA;;;" + sidA + ")(A;OICIID;FR;;;BU)"
		}},
		// Repair and protection.
		{"directory already protected", "G6/legacy-repair: the directory was not the declared legacy account-owned, openly writable directory before the first start", func(rs []Record) {
			b := ev(t, rs, "G6/legacy-repair").DaemonLog.Before.Dir
			b.DACL = protectedFor(sidA)
		}},
		{"repair left the DACL open", "G6/legacy-repair: the daemon directory is not protected for its manager only", func(rs []Record) {
			ev(t, rs, "G6/legacy-repair").DaemonLog.After.Dir.DACL = "D:AI(A;OICI;FA;;;" + sidA + ")"
		}},
		{"system log readable by the user", "G6/system-protection: the current log is not protected for its manager only", func(rs []Record) {
			ev(t, rs, "G6/system-protection").DaemonLog.After.Current.DACL = protectedFor(sidA)
		}},
		// Peer denial.
		{"peer log readable", "G6/peer-denial: path peer-log result ok=true", func(rs []Record) {
			ev(t, rs, "G6/peer-denial").Paths[2] = PathResult{Probe: "peer-log", OK: true}
		}},
		{"denial probed as SYSTEM", "G6/peer-denial: the probe did not run under the account's own wts token", func(rs []Record) {
			tp := ev(t, rs, "G6/peer-denial").Token
			tp.Source = productTokenSource
			tp.Session, tp.LogonType = 0, logonNetwork
			rs[findRecord(t, rs, "G6/peer-denial")].Token.Session = 0
		}},
		// Named tests in the account's own session.
		{"go tests run by SYSTEM", "G6/go-tests: the runner is not the account's own wts token", func(rs []Record) {
			ev(t, rs, "G6/go-tests").TestRun.Runner.Token = TokenFacts{SID: SystemSID}
		}},
		{"one go test missing", "G6/go-tests: the named test did not run once and pass", func(rs []Record) {
			tr := ev(t, rs, "G6/go-tests").TestRun
			tr.Events = tr.Events[4:]
		}},
		{"identity test skipped", "G6/go-tests: the test or a subtest was skipped", func(rs []Record) {
			tr := ev(t, rs, "G6/go-tests").TestRun
			tr.Events[len(tr.Events)-1].Action = "skip"
		}},
		// Independent permissions.
		{"linger lost on admission change", "H06/A: lingerKept below its minimum", func(rs []Record) {
			gen(ev(t, rs, "H06/A").Observer, RoleManager, AccountA, 0).Exited = ft(20)
		}},
		{"session manager killed by the linger change", "H06/B: sessionRetained below its minimum", func(rs []Record) {
			for _, g := range gensOf(ev(t, rs, "H06/B").Observer, RoleManager, AccountB) {
				if g.Token.Session != 0 {
					g.Exited = ft(31)
				}
			}
		}},
		{"no session at the linger change", "H06/A: sessionRetained below its minimum", func(rs []Record) {
			r := ev(t, rs, "H06/A").Observer
			r.Sessions = r.Sessions[:1]
		}},
		{"relaunch after logoff", "H06/B: negativeSec below its minimum", func(rs []Record) {
			r := ev(t, rs, "H06/B").Observer
			r.Generations = append(r.Generations, Generation{Role: RoleManager, Account: AccountB, PID: 9, Created: ft(80), Seen: ft(80.05), Exited: ft(81),
				Token: tokenFacts(AccountB, ModeS4U)})
		}},
		{"marks out of order", "H06/A: lingerKept not supported by the evidence", func(rs []Record) {
			ev(t, rs, "H06/A").Observer.Marks[1].At = ft(5)
		}},
		// Final inventory.
		{"no inventory", "H22/all: no final inventory", func(rs []Record) { ev(t, rs, "H22/all").Inventory = nil }},
		{"inventory of a baseline name only", "H22/all: the inventory's baseline is not the one the first-use check recorded", func(rs []Record) {
			*ev(t, rs, "H22/all").Inventory = InventoryProof{Baseline: InventoryBaseline{Name: "c5-baseline"}, At: ft(90000)}
		}},
		{"scope omitted", "H22/all: the inventory does not cover every account, image and resource of the run", func(rs []Record) {
			ev(t, rs, "H22/all").Inventory.Scope = InventoryScope{}
		}},
		{"test binaries not inventoried", "H22/all: the inventory does not cover every account, image and resource", func(rs []Record) {
			sc := &ev(t, rs, "H22/all").Inventory.Scope
			sc.Images = slices.DeleteFunc(sc.Images, func(s string) bool { return s == "runtime.test.exe" })
		}},
		{"an account not inventoried", "H22/all: the inventory does not cover every account, image and resource", func(rs []Record) {
			sc := &ev(t, rs, "H22/all").Inventory.Baseline.Scope
			sc.Accounts = sc.Accounts[:2]
		}},
		{"firewall not read", "H22/all: the inventory does not cover every account, image and resource", func(rs []Record) {
			sc := &ev(t, rs, "H22/all").Inventory.Scope
			sc.Resources = slices.DeleteFunc(sc.Resources, func(s string) bool { return s == ResourceFirewall })
		}},
		{"unrelated baseline", "H22/all: the inventory's baseline is not the one the first-use check recorded", func(rs []Record) {
			ev(t, rs, "H22/all").Inventory.Baseline.Name = "other-baseline"
		}},
		{"baseline receipt replaced", "H22/all: the inventory's baseline is not the one the first-use check recorded", func(rs []Record) {
			ev(t, rs, "H22/all").Inventory.BaselineSHA256 = strings.Repeat("1", 64)
		}},
		{"no first-use check", "H22/all: no first-use check recorded the baseline", func(rs []Record) { ev(t, rs, "H01/A#first-use").FirstUse = nil }},
		{"baseline after the first case", "H22/all: the baseline was not taken before the first case", func(rs []Record) {
			ev(t, rs, "H22/all").Inventory.Baseline.At = ft(-3000)
		}},
		{"inventory before the last observation", "H22/all: the inventory was not taken after the last observation", func(rs []Record) {
			ev(t, rs, "H22/all").Inventory.At = ft(100)
		}},
		{"service missing at both ends", "H22/all: the winunitd service is not installed", func(rs []Record) {
			inv := ev(t, rs, "H22/all").Inventory
			inv.Facts.Service, inv.Baseline.Facts.Service = ServiceFacts{}, ServiceFacts{}
		}},
		{"no broker", "H22/all: the service control manager names no running winunitd service process", func(rs []Record) {
			ev(t, rs, "H22/all").Inventory.Broker = nil
		}},
		{"a user daemon as the broker", "H22/all: the service control manager names no running winunitd service process", func(rs []Record) {
			ev(t, rs, "H22/all").Inventory.Broker.SID = sidA
		}},
		{"workload left running", "H22/all: owned processes remain", func(rs []Record) {
			ev(t, rs, "H22/all").Inventory.Processes = []InventoryProcess{{Image: workloadImage, SID: sidB, PID: 7}}
		}},
		{"fixture pipe left", "H22/all: fixture pipes remain", func(rs []Record) { ev(t, rs, "H22/all").Inventory.Pipes = []string{`winunitd-qual\a`} }},
		{"grant left behind", "H22/all: the machine state differs from the baseline", func(rs []Record) { ev(t, rs, "H22/all").Inventory.Facts.Grants = []string{sidA} }},
		{"task left behind", "H22/all: the machine state differs from the baseline", func(rs []Record) {
			ev(t, rs, "H22/all").Inventory.Facts.Tasks = []string{`\winunitd-qual\observer`}
		}},
		{"template not restored", "H22/all: the machine state differs from the baseline", func(rs []Record) {
			ev(t, rs, "H22/all").Inventory.Facts.TemplateFiles = []string{"background.service"}
		}},
		{"firewall rule left", "H22/all: the machine state differs from the baseline", func(rs []Record) {
			ev(t, rs, "H22/all").Inventory.Facts.FirewallRules = []string{"winunitd-qual-echo"}
		}},
		{"service recovery changed", "H22/all: the machine state differs from the baseline", func(rs []Record) {
			ev(t, rs, "H22/all").Inventory.Facts.Service.Recovery = "0/0;reset/0"
		}},
		{"admission policy not restored", "H22/all: the machine state differs from the baseline", func(rs []Record) {
			ev(t, rs, "H22/all").Inventory.Facts.Admission.SHA256 = strings.Repeat("2", 64)
		}},
		{"admission policy removed", "H22/all: the machine state differs from the baseline", func(rs []Record) {
			ev(t, rs, "H22/all").Inventory.Facts.Admission = nil
		}},
		{"data root opened", "H22/all: the machine state differs from the baseline", func(rs []Record) {
			ev(t, rs, "H22/all").Inventory.Facts.DataDir.DACL += "(A;OICI;FA;;;BU)"
		}},
		{"data root not read", "H22/all: the data root's security was not read", func(rs []Record) {
			inv := ev(t, rs, "H22/all").Inventory
			inv.Facts.Linger, inv.Baseline.Facts.Linger = nil, nil
		}},
		{"failed inventory query", "H22/all: an inventory query failed", func(rs []Record) {
			ev(t, rs, "H22/all").Inventory.Errors = []NativeError{{Op: "tasks", Win32: 2}}
		}},
	})
}

func TestProtectedDACL(t *testing.T) {
	if !protectedDACL("D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;"+sidA+")", sidA) || !protectedDACL("D:PAI(A;;FA;;;"+sidA+")(A;;FA;;;BA)(A;;FA;;;SY)", sidA) {
		t.Fatal("the protected DACL rejected")
	}
	if !protectedDACL("D:P(A;;FA;;;SY)(A;;FA;;;BA)", SystemSID) {
		t.Fatal("the machine DACL rejected")
	}
	for name, dacl := range map[string]string{
		"not protected":       "D:(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;" + sidA + ")",
		"extra reader":        "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;" + sidA + ")(A;;FR;;;BU)",
		"another account":     "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;" + sidB + ")",
		"inherited grant":     "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;OICIID;FA;;;" + sidA + ")",
		"missing SYSTEM":      "D:P(A;;FA;;;BA)(A;;FA;;;" + sidA + ")",
		"not a DACL":          "O:SY",
		"user for the system": "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;" + sidA + ")",
	} {
		sid := sidA
		if name == "user for the system" {
			sid = SystemSID
		}
		if protectedDACL(dacl, sid) {
			t.Errorf("%s accepted", name)
		}
	}
}

// A crash loop needs failed launches, a capped delay within its tolerance
// and recovery; a cancellation needs a recovery that was actually waiting.
// spread relaunches an account's failing managers every gap seconds and
// moves the quiet mark and the observation end after the last failure.
func spread(r *ObserverReport, account string, gap float64) {
	gs := gensOf(r, RoleManager, account)
	for i, g := range gs {
		at := ft(float64(i) * gap)
		life, seen, crash := g.Exited-g.Created, g.Seen-g.Created, g.Exited-g.Crashed
		g.Created, g.Seen, g.Exited, g.Crashed = at, at+seen, at+life, at+life-crash
	}
	r.Marks[0].At = gs[len(gs)-1].Exited + 10e7
	r.Ended = r.Marks[0].At + 200e7
}

func TestSummarizeRequiresFailureAndWaiting(t *testing.T) {
	succeeded := func(r *ObserverReport, account string) {
		for _, g := range gensOf(r, RoleManager, account) {
			g.Crashed, g.ExitCode = 0, 0
		}
	}
	runMutations(t, []mutationCase{
		{"crash loop of successful exits", "G1/B: failures below its minimum", func(rs []Record) { succeeded(ev(t, rs, "G1/B").Observer, AccountB) }},
		{"headless loop of successful exits", "G2/A: failures below its minimum", func(rs []Record) { succeeded(ev(t, rs, "G2/A").Observer, AccountA) }},
		{"cancellation with nothing waiting", "G4/linger-A: failedBeforeQuiet not supported by the evidence", func(rs []Record) {
			ev(t, rs, "G4/linger-A").Observer.Generations = nil
		}},
		{"cancellation long after the last failure", "G4/stop-B: waitingAtQuiet below its minimum", func(rs []Record) {
			r := ev(t, rs, "G4/stop-B").Observer
			r.Marks[0].At += 200e7
			r.Ended += 200e7
		}},
		{"cancellation before the cap", "G4/linger-B: cappedBeforeQuiet below its minimum", func(rs []Record) {
			r := ev(t, rs, "G4/linger-B").Observer
			gs := gensOf(r, RoleManager, AccountB)
			last := gs[len(gs)-1]
			life := last.Exited - last.Created
			last.Created = gs[len(gs)-2].Created + 40e7
			last.Seen, last.Exited, last.Crashed = last.Created+5e5, last.Created+life, last.Created+life-1000
		}},
		{"waiting failures 600 s apart", "G4/linger-A: overlongGaps above its maximum", func(rs []Record) {
			spread(ev(t, rs, "G4/linger-A").Observer, AccountA, 600)
		}},
		{"waiting failures 600 s apart, not capped", "G4/linger-A: cappedBeforeQuiet below its minimum", func(rs []Record) {
			spread(ev(t, rs, "G4/linger-A").Observer, AccountA, 600)
		}},
		{"no growth before the cap", "G4/stop-A: grewBeforeQuiet below its minimum", func(rs []Record) {
			spread(ev(t, rs, "G4/stop-A").Observer, AccountA, 60)
		}},
		{"stop without a broker restart", "G4/stop-A: observer: the broker was not stopped and restarted inside the window", func(rs []Record) {
			r := ev(t, rs, "G4/stop-A").Observer
			r.Generations = slices.DeleteFunc(r.Generations, func(g Generation) bool { return g.Role == RoleBroker && g.Exited == 0 })
		}},
		{"broker crashed instead of stopping", "G4/stop-B: observer: the broker was not stopped and restarted inside the window", func(rs []Record) {
			for i := range ev(t, rs, "G4/stop-B").Observer.Generations {
				if g := &ev(t, rs, "G4/stop-B").Observer.Generations[i]; g.Role == RoleBroker && g.Exited != 0 {
					crash(g)
				}
			}
		}},
		{"cancellation after the retry was due", "G4/stop-B: waitingAtQuiet below its minimum", func(rs []Record) {
			r := ev(t, rs, "G4/stop-B").Observer
			r.Marks[0].At += 55e7
			r.Ended += 55e7
		}},
		{"capped gap far past the cap", "G2/B: overlongGaps above its maximum", func(rs []Record) {
			r := ev(t, rs, "G2/B").Observer
			gs := gensOf(r, RoleManager, AccountB)
			for _, g := range gs[10:] {
				g.Created += 30e7
				g.Seen += 30e7
				if g.Exited != 0 {
					g.Exited += 30e7
					g.Crashed += 30e7
				}
			}
			r.Ended += 30e7
		}},
	})
}

// An observation that did not happen is unknown, not zero, and only an
// observer running as SYSTEM over the admitted images counts.
func TestSummarizeRequiresCompleteObservation(t *testing.T) {
	shared := func(rs []Record, keys []string, fn func(*ObserverReport)) {
		for _, k := range keys {
			fn(ev(t, rs, k).Observer)
		}
	}
	coldBoot := []string{"H01/A", "H02/B"}
	runMutations(t, []mutationCase{
		{"no sampling record", "H13/A: observer: the observer's session and logon sampling is incomplete", func(rs []Record) {
			ev(t, rs, "H13/A").Observer.Sampling = nil
		}},
		{"failed session query", "H01/A: observer: the observer's session and logon sampling is incomplete", func(rs []Record) {
			shared(rs, coldBoot, func(r *ObserverReport) { r.Sampling.Errors = []NativeError{{Op: "session-user", Win32: 5}} })
		}},
		{"one sample", "H14/B: observer: the observer's session and logon sampling is incomplete", func(rs []Record) {
			ev(t, rs, "H14/B").Observer.Sampling.Samples = 1
		}},
		{"no audited history", "H17/A: phase fresh-boot has no complete observed logon history since its boot", func(rs []Record) {
			ev(t, rs, "H17/A").Observer.Audit = nil
		}},
		{"audit log cleared", "H07/B: phase lifecycle has no complete observed logon history since its boot", func(rs []Record) {
			ev(t, rs, "H07/B").Observer.Audit.ClearedSinceBoot = true
		}},
		{"audit log begins after the boot", "H18/B: phase fresh-boot has no complete observed logon history since its boot", func(rs []Record) {
			r := ev(t, rs, "H18/B").Observer
			r.Audit.Oldest = r.Boot.Time + 1
		}},
		{"audited password logon", "H13/B: phase fresh-boot ran after a password-bearing logon", func(rs []Record) {
			r := ev(t, rs, "H13/B").Observer
			r.Logons = []LogonFact{{ID: "1", SID: sidB, Type: logonInteractive, LogonTime: r.Boot.Time + 1, Source: "audit", Process: "User32"}}
		}},
		{"observer not SYSTEM", "H08/A: observer: the observer did not run as SYSTEM in session zero", func(rs []Record) {
			ev(t, rs, "H08/A").Observer.Observer.SID = sidA
		}},
		{"observer in a session", "H05/B: observer: the observer did not run as SYSTEM in session zero", func(rs []Record) {
			ev(t, rs, "H05/B").Observer.Observer.Session = 1
		}},
		{"another daemon watched", "H09/A: the observer watched images other than the admitted winunitd.exe and headless-workload.exe", func(rs []Record) {
			shared(rs, []string{"H09/A", "H09/B"}, func(r *ObserverReport) { r.Images.Daemon = testOtherSHA })
		}},
	})
}

// The product's own S4U logons, which the audit shows as a batch or
// network logon by its logon process, are not password-bearing logons.
func TestProductS4ULogonsAreNotPasswordLogons(t *testing.T) {
	m := testMatrix(t)
	rs := cloneRecords(allPassing(t, m))
	r := ev(t, rs, "H07/A").Observer
	r.Logons = []LogonFact{{ID: "2", SID: sidA, Type: logonBatch, LogonTime: r.Boot.Time + 1, Source: "audit", Process: productTokenSource}}
	if s := Summarize(m, rs, testRun(t), Selection{}); !s.Complete {
		t.Fatalf("product S4U logon counted: %q", s.Problems)
	}
}

// The outside-unit client is the account outside the probed unit: another
// unit's process is outside it, a process of the same unit is not.
func TestOutsideUnitClient(t *testing.T) {
	m := testMatrix(t)
	run := testRun(t)
	base := allPassing(t, m)
	move := func(parent func(r *ObserverReport, workload *Generation) uint32, role string) Summary {
		rs := cloneRecords(base)
		e := ev(t, rs, "H15/A")
		r := e.Observer
		workload := gensOf(r, RoleWorkload, AccountA)[0]
		for i := range r.Generations {
			g := &r.Generations[i]
			if g.PID == e.Pipe[1].PID && g.Created == e.Pipe[1].Created {
				g.ParentPID, g.Role = parent(r, workload), role
			}
		}
		return Summarize(m, rs, run, Selection{})
	}
	sameUnit := move(func(_ *ObserverReport, w *Generation) uint32 { return w.PID }, RoleChild)
	if !strings.Contains(strings.Join(sameUnit.Problems, "\n"), "H15/A: pipe the outside-unit client was a process of the account's unit") {
		t.Fatalf("a client of the same unit counted as outside: %q", sameUnit.Problems)
	}
	otherUnit := move(func(r *ObserverReport, _ *Generation) uint32 { return gensOf(r, RoleManager, AccountA)[0].PID }, RoleWorkload)
	if !otherUnit.Complete {
		t.Fatalf("another unit's client not counted as outside: %q", otherUnit.Problems)
	}
}
