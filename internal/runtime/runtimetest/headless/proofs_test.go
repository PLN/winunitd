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
			r.Logons = []LogonFact{{ID: "1", SID: sidA, Type: logonInteractive, LogonTime: ft(1)}}
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
		{"manager not restarted", "H12/A: the observer held no manager of the account started after the intervention and still running", func(rs []Record) {
			gen(ev(t, rs, "H12/A").Observer, RoleManager, AccountA, 0).Created = ft(5)
		}},
		{"session lane under an elevated token", "G6/filtered-admin: the observer held no manager of the account started after the intervention", func(rs []Record) {
			gen(ev(t, rs, "G6/filtered-admin").Observer, RoleManager, AccountAdmin, 0).Token.ElevationType = 1
		}},
		{"proof of another account", "H12/B: the daemon-log proof names another account", func(rs []Record) { ev(t, rs, "H12/B").DaemonLog.SID = sidA }},
		{"failed daemon-log query", "H12/A: a daemon-log query failed", func(rs []Record) {
			ev(t, rs, "H12/A").DaemonLog.After.Errors = []NativeError{{Op: "archive", Win32: 5}}
		}},
		// Repair and protection.
		{"directory already protected", "G6/legacy-repair: the directory was not a legacy account-owned directory", func(rs []Record) {
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
		{"grant left behind", "H22/all: qualification linger grants remain", func(rs []Record) { ev(t, rs, "H22/all").Inventory.Grants = []string{sidA} }},
		{"workload left running", "H22/all: owned processes remain", func(rs []Record) {
			ev(t, rs, "H22/all").Inventory.Processes = []InventoryProcess{{Image: workloadImage, SID: sidB, PID: 7}}
		}},
		{"task left behind", "H22/all: fixture scheduled tasks remain", func(rs []Record) { ev(t, rs, "H22/all").Inventory.Tasks = []string{`\winunitd-qual\observer`} }},
		{"template not restored", "H22/all: fixture files in the Default profile template remain", func(rs []Record) {
			ev(t, rs, "H22/all").Inventory.TemplateFiles = []string{"background.service"}
		}},
		{"service recovery changed", "H22/all: the winunitd service configuration differs from the baseline", func(rs []Record) {
			ev(t, rs, "H22/all").Inventory.Service.Recovery = "none/0"
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
