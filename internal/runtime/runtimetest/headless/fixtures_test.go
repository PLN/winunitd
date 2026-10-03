package headless

import (
	"encoding/json"
	"hash/fnv"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/protocol"
)

const (
	testSource  = "0123456789abcdef0123456789abcdef01234567"
	sidA        = "S-1-5-21-1-2-3-1001"
	sidB        = "S-1-5-21-1-2-3-1002"
	sidAdmin    = "S-1-5-21-1-2-3-500"
	sidControl  = "S-1-5-21-9-9-9-1101"
	testNonce   = "00112233445566778899aabbccddeeff"
	testContent = "c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00"
	// Admitted hashes of the runtime test binary and an unrelated program.
	testRuntimeSHA = "7e57000000000000000000000000000000000000000000000000000000000001"
	testOtherSHA   = "0e00000000000000000000000000000000000000000000000000000000000002"
	testJournalSHA = "7e57000000000000000000000000000000000000000000000000000000000003"
	testDaemonSHA  = "da00000000000000000000000000000000000000000000000000000000000004"
	testShare      = `\\peer\share\nonce.txt`
	testEFSFile    = `C:\Users\b\efs\secret.txt`
)

var testAdmission = sync.OnceValues(func() ([]byte, error) {
	exe, err := ExecutableSHA256()
	if err != nil {
		return nil, err
	}
	return json.Marshal(Admission{Schema: AdmissionSchema, Source: testSource, Artifacts: []Artifact{
		{Name: workloadImage, SHA256: exe}, {Name: daemonImage, SHA256: testDaemonSHA}, {Name: "runtime.test.exe", SHA256: testRuntimeSHA},
		{Name: "journal.test.exe", SHA256: testJournalSHA},
		{Name: "other.exe", SHA256: testOtherSHA}}})
})

func testRun(t *testing.T) AdmittedRun {
	t.Helper()
	data, err := testAdmission()
	if err != nil {
		t.Fatal(err)
	}
	run, err := DecodeAdmission(data)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func writeTestAdmission(t *testing.T) string {
	t.Helper()
	data, err := testAdmission()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "admission.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testMatrix(t *testing.T) *Matrix {
	t.Helper()
	m, err := CaseMatrix()
	if err != nil {
		t.Fatal(err)
	}
	return m
}

var roleSID = map[string]string{AccountA: sidA, AccountB: sidB, AccountAdmin: sidAdmin, AccountControl: sidControl}

func tokenFor(account, mode string) *Token {
	switch mode {
	case ModeS4U:
		return &Token{SID: roleSID[account], Source: SourceS4U}
	case ModeWTS:
		return &Token{SID: roleSID[account], Session: 2, Source: SourceWTS}
	case ModeSystem:
		return &Token{SID: SystemSID, Elevated: true, Source: SourceProcess}
	case ModeFilteredAdmin:
		return &Token{SID: sidAdmin, Session: 2, Source: SourceProcess}
	case ModePassword:
		return &Token{SID: roleSID[account], Session: 3, Source: SourcePassword}
	case ModePeer:
		return &Token{Source: SourcePeer}
	}
	return nil
}

var t0 = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

func at(sec float64) time.Time { return t0.Add(time.Duration(sec * float64(time.Second))) }

// launches builds short-lived failing attempts at the given gaps.
func launches(gaps ...float64) []Attempt {
	var out []Attempt
	now := 0.0
	for i := 0; i <= len(gaps); i++ {
		if i > 0 {
			now += gaps[i-1]
		}
		out = append(out, Attempt{Launched: at(now), Exited: at(now + 0.5), ExitCode: 1, PID: uint32(100 + i), Created: uint64(1000 + i)})
	}
	return out
}

// Observer report fixtures. ft(sec) is the FILETIME sec seconds after t0.
var ft0 = uint64(t0.UnixNano()/100) + 116444736000000000

func ft(sec float64) uint64 { return uint64(int64(ft0) + int64(sec*1e7)) }

// testBoot is the boot of a phase; phase 1 is one fresh boot.
func testBoot(phase int) Boot { return Boot{Time: ft(-3600), Counter: uint32(phase)} }

// firstUseBoot is the boot the first-use check runs on, before phase 1's.
var firstUseBoot = Boot{Time: ft(-7200), Counter: 0}

var testExecutable = sync.OnceValue(func() string {
	exe, _ := ExecutableSHA256()
	return exe
})

func tokenFacts(account, mode string) TokenFacts {
	switch mode {
	case ModeWTS:
		return TokenFacts{SID: roleSID[account], Session: 2, Source: "User32", LogonType: logonInteractive, AuthPackage: "Negotiate", AuthenticationID: "00000000:00020000",
			ElevationType: 1}
	case ModeFilteredAdmin:
		return TokenFacts{SID: roleSID[account], Session: 2, Source: "User32", LogonType: logonInteractive, AuthPackage: "Negotiate", AuthenticationID: "00000000:00040000",
			ElevationType: tokenElevationTypeLimit}
	}
	return TokenFacts{SID: roleSID[account], Source: productTokenSource, LogonType: logonNetwork, AuthPackage: "Kerberos", AuthenticationID: "00000000:00010000"}
}

// observed builds a finished observer report for phase that scanned from
// 5 seconds before t0 until end.
type observed struct {
	*ObserverReport
	pid uint32
}

func newObserved(phase int, end float64) *observed {
	boot := testBoot(phase)
	return &observed{ObserverReport: &ObserverReport{Schema: ObserverSchema, Executable: testExecutable(), Boot: boot,
		Accounts: map[string]string{AccountA: sidA, AccountB: sidB}, Started: ft(-5), Ended: ft(end), FinalScan: ft(end), Scans: 1000, MaxGap: 500000,
		Observer: ObserverIdentity{PID: 500, Created: ft(-3000), SID: SystemSID},
		Images:   ObservedImages{Daemon: testDaemonSHA, Workload: testExecutable()},
		// One sample a second from the first scan to the final one, the
		// first with the initial session state.
		Sampling: &SamplingFacts{Samples: int(math.Ceil(end+5)) + 1, First: ft(-5), Last: ft(end), MaxInterval: 1e7},
		Sessions: []SessionSample{{At: ft(-5)}},
		Audit: &AuditFacts{Read: true, Oldest: boot.Time - 1e7, PolicyStart: &AuditPolicy{LogonSuccess: true, PolicyChangeSuccess: true},
			PolicyEnd: &AuditPolicy{LogonSuccess: true, PolicyChangeSuccess: true}, To: ft(end + 3)},
		Stage: ObserverFinished}, pid: 1000}
}

// gen adds a generation created at sec; it is seen at the first scan or
// 50 ms after its creation, and exits at exit unless that is negative. The
// pointer is valid until the next gen.
func (o *observed) gen(role, account, mode string, created, exit float64, code uint32) *Generation {
	o.pid++
	g := Generation{Role: role, Account: account, PID: o.pid, Created: ft(created), Seen: max(ft(created+0.05), o.Started), Token: tokenFacts(account, mode)}
	if role == RoleBroker {
		g.Account, g.Token = "", TokenFacts{SID: SystemSID, Source: "*SYSTEM*", AuthenticationID: "00000000:000003e7"}
	}
	if exit >= 0 {
		g.Exited, g.ExitCode = ft(exit), code
	}
	o.Generations = append(o.Generations, g)
	return &o.Generations[len(o.Generations)-1]
}

// crash marks a generation as terminated by the observer just before its exit.
func crash(g *Generation) { g.Crashed, g.ExitCode = g.Exited-1000, 0xdead }

// stable adds the account's manager and workload running throughout and
// returns the workload's PID.
func (o *observed) stable(account string) uint32 {
	m := o.gen(RoleManager, account, ModeS4U, -60, -1, 0).PID
	w := o.gen(RoleWorkload, account, ModeS4U, -59, -1, 0)
	w.ParentPID = m
	return w.PID
}

// child adds a short-lived process started by parent, such as a probe.
func (o *observed) child(account string, parent uint32, created float64) Generation {
	g := o.gen(RoleChild, account, ModeS4U, created, created+1, 0)
	g.ParentPID = parent
	return *g
}

func peerOf(account string) string {
	if account == AccountA {
		return AccountB
	}
	return AccountA
}

// loop adds crashed manager generations at the attempts' times, then the
// recovered manager that keeps running.
func (o *observed) loop(account, mode string, att []Attempt) {
	for _, a := range att {
		crash(o.gen(RoleManager, account, mode, a.Launched.Sub(t0).Seconds(), a.Exited.Sub(t0).Seconds(), 0))
	}
	o.gen(RoleManager, account, mode, lastLaunch(att)+60, -1, 0)
	if end := lastLaunch(att) + 70; ft(end) > o.Ended {
		o.endAt(end)
	}
}

// endAt moves the final scan to sec, with sampling and the audit read
// keeping up with it.
func (o *observed) endAt(sec float64) {
	o.Ended, o.FinalScan = ft(sec), ft(sec)
	o.Sampling.Samples, o.Sampling.Last = int(math.Ceil(sec+5))+1, ft(sec)
	o.Audit.To = ft(sec + 3)
}

func lastLaunch(att []Attempt) float64 { return att[len(att)-1].Launched.Sub(t0).Seconds() }

// tokenProbeOf is the token a held process reports about itself.
func tokenProbeOf(g Generation) *TokenProbe {
	return &TokenProbe{PID: g.PID, Created: g.Created, SID: g.Token.SID, Source: g.Token.Source, Session: g.Token.Session, Elevated: g.Token.Elevated,
		AuthenticationID: g.Token.AuthenticationID, LogonType: g.Token.LogonType, AuthPackage: g.Token.AuthPackage, Integrity: "medium", ElevationType: 1}
}

// passwordProbe is a same-account password-bearing logon's token.
func passwordProbe(account string) *TokenProbe {
	return &TokenProbe{PID: 4000, Created: ft(-1), SID: roleSID[account], Source: "User32", Session: 3, LogonType: logonInteractive,
		AuthPackage: "Negotiate", AuthenticationID: "00000000:00030000", Integrity: "medium", ElevationType: 1}
}

// probed is an observer report holding the account's unit, with the probe
// process and any further in-unit processes, and an outside process.
type probed struct {
	report        *ObserverReport
	probe, inUnit Generation
	outside       Generation
}

func probeReport(e Entry) probed {
	o := newObserved(e.Phase, 30)
	w := o.stable(e.Account)
	var p probed
	p.probe = o.child(e.Account, w, 1)
	p.inUnit = o.child(e.Account, w, 2)
	p.outside = o.child(e.Account, 0, 3)
	p.report = o.ObserverReport
	return p
}

func pipeServers(e Entry, p probed) []ServerReport {
	sid, peer := roleSID[e.Account], roleSID[peerOf(e.Account)]
	server := ServerIdentity{PID: 600, Created: ft(-30), SID: SystemSID}
	entry := func(pid uint32, created uint64, processSID string, claim *Claim, exited bool) ServerEntry {
		o := CallerObservation{PID: pid, Created: created, ProcessSID: processSID, ImpersonationSID: processSID, Claim: claim, Exited: exited}
		ok, reason := Decide(sid, o)
		v := Verdict{Accepted: ok, Reason: reason, Held: true}
		if ok {
			v.PID, v.Created = pid, created
		}
		return ServerEntry{Observation: o, Verdict: v}
	}
	own := ServerReport{Name: QualificationPipePrefix + "h15", Allowed: sid, ACL: []string{sid}, Server: server, Entries: []ServerEntry{
		entry(p.inUnit.PID, p.inUnit.Created, sid, &Claim{PID: p.inUnit.PID, Created: p.inUnit.Created, SID: sid}, false),
		entry(p.outside.PID, p.outside.Created, sid, nil, false),
		entry(p.inUnit.PID, p.inUnit.Created, sid, &Claim{PID: p.inUnit.PID, Created: p.inUnit.Created + 1}, false),
		entry(5000, ft(4), sid, &Claim{PID: 5000, Created: ft(4)}, true),
	}}
	// The stale claim comes from another connection of the same in-unit
	// process; give it its own incarnation so each client has one entry.
	own.Entries[2].Observation.PID, own.Entries[2].Observation.Created = 5001, ft(5)
	own.Entries[2].Observation.Claim = &Claim{PID: 5001, Created: ft(5) + 1}
	own.Entries[2].Verdict = Verdict{Reason: ReasonClaim, Held: true}
	wide := ServerReport{Name: QualificationPipePrefix + "h15-wide", Allowed: sid, ACL: []string{sid, peer}, Server: server, Entries: []ServerEntry{
		entry(5002, ft(6), peer, nil, false),
	}}
	return []ServerReport{own, wide}
}

// observerFor builds the report an observed case's records carry. Shared
// executions get the same report for every account.
func observerFor(e Entry) *ObserverReport {
	switch e.Case {
	case "G1", "G2":
		att := launches(1, 2, 4, 8, 16, 32, 60, 61, 60, 62, 60, 60, 63, 60, 60)
		o := newObserved(e.Phase, lastLaunch(att)+5)
		o.stable(peerOf(e.Account))
		o.loop(e.Account, e.Mode, att)
		return o.ObserverReport
	case "G3":
		att := launches(1, 2, 4, 8, 16, 32)
		last := att[len(att)-1].Launched
		stable := Attempt{Launched: last.Add(32 * time.Second), Exited: last.Add(162 * time.Second)}
		r1 := Attempt{Launched: stable.Exited.Add(5 * time.Second), Exited: stable.Exited.Add(5500 * time.Millisecond)}
		r2 := Attempt{Launched: r1.Launched.Add(time.Second), Exited: r1.Launched.Add(1500 * time.Millisecond)}
		r3 := Attempt{Launched: r2.Launched.Add(2 * time.Second), Exited: r2.Launched.Add(2500 * time.Millisecond)}
		att = append(att, stable, r1, r2, r3)
		o := newObserved(e.Phase, lastLaunch(att)+5)
		for _, a := range att {
			crash(o.gen(RoleManager, e.Account, e.Mode, a.Launched.Sub(t0).Seconds(), a.Exited.Sub(t0).Seconds(), 0))
		}
		return o.ObserverReport
	case "G4":
		att := launches(1, 2, 4, 8, 16, 32, 60)
		end := lastLaunch(att)
		o := newObserved(e.Phase, end+140)
		for _, a := range att {
			crash(o.gen(RoleManager, e.Account, e.Mode, a.Launched.Sub(t0).Seconds(), a.Exited.Sub(t0).Seconds(), 0))
		}
		o.Marks = []Mark{{Name: "quiet", At: ft(end + 10)}}
		if strings.HasPrefix(e.Variant, "stop-") {
			// The broker stops after the grant is revoked and starts again.
			o.gen(RoleBroker, "", ModeSystem, -3000, end+12, 0)
			o.gen(RoleBroker, "", ModeSystem, end+14, -1, 0)
		}
		return o.ObserverReport
	case "G5":
		gaps := []float64{0.6, 0.7, 0.9, 1.3}
		for range 400 {
			gaps = append(gaps, 1.5)
		}
		att := launches(gaps...)
		o := newObserved(e.Phase, lastLaunch(att)+5)
		m := o.gen(RoleManager, e.Account, e.Mode, -60, -1, 0).PID
		for _, a := range att {
			o.gen(RoleWorkload, e.Account, e.Mode, a.Launched.Sub(t0).Seconds(), a.Exited.Sub(t0).Seconds(), 7).ParentPID = m
		}
		o.gen(RoleWorkload, e.Account, e.Mode, lastLaunch(att)+1.5, -1, 0).ParentPID = m
		o.Releases = []Mark{{Name: "release", At: ft(lastLaunch(att) + 1)}}
		return o.ObserverReport
	case "H01", "H02":
		// One cold boot for both accounts: A's profile is created on this
		// boot, B's existed before it.
		o := newObserved(e.Phase, 60)
		o.Sessions = []SessionSample{{At: o.Started}}
		o.Profiles = map[string]ProfileFacts{
			AccountA: {Registered: true, DirectoryCreated: ft(-50), HiveLoaded: true},
			AccountB: {Registered: true, DirectoryCreated: ft(-90000), HiveLoaded: true},
		}
		o.Progress = map[string]Progress{}
		for _, acct := range []string{AccountA, AccountB} {
			m := o.gen(RoleManager, acct, ModeS4U, -40, -1, 0).PID
			w := o.gen(RoleWorkload, acct, ModeS4U, -39, -1, 0)
			w.ParentPID = m
			o.Progress[acct] = Progress{Sequence: 9, PID: w.PID, Created: w.Created, SID: roleSID[acct], AuthenticationID: w.Token.AuthenticationID}
		}
		return o.ObserverReport
	case "H03":
		return newObserved(e.Phase, 130).ObserverReport
	case "H06":
		// Admission revoked at 10 s and restored at 15 s, a logon at 20 s,
		// linger disabled at 30 s, logoff at 50 s; everything drains and
		// stays gone.
		o := newObserved(e.Phase, 140)
		o.stable(peerOf(e.Account))
		sid := roleSID[e.Account]
		m := o.gen(RoleManager, e.Account, ModeS4U, -60, 35, 1).PID
		o.gen(RoleWorkload, e.Account, ModeS4U, -59, 35, 1).ParentPID = m
		o.gen(RoleManager, e.Account, ModeWTS, 20, 51, 0)
		o.Marks = []Mark{{Name: MarkAdmissionRevoked, At: ft(10)}, {Name: MarkAdmissionRestored, At: ft(15)}, {Name: MarkLingerDisabled, At: ft(30)},
			{Name: MarkLogoff, At: ft(50)}}
		o.Sessions = []SessionSample{{At: o.Started}, {At: ft(20), Users: []SessionUser{{Session: 2, SID: sid}}}, {At: ft(50.5)}}
		o.Logons = []LogonFact{{ID: "00000000:00020000", SID: sid, Type: logonInteractive, LogonTime: ft(19), Source: "audit", Process: "User32"}}
		return o.ObserverReport
	case "H12", "G6":
		// The manager the daemon-log proof needs started after the
		// intervention at 10 s and still runs.
		o := newObserved(e.Phase, 60)
		if e.Account == AccountAdmin {
			o.Accounts[AccountAdmin] = sidAdmin
		}
		o.Profiles = map[string]ProfileFacts{e.Account: {Path: profileOf(e.Account), Registered: true, DirectoryCreated: ft(-90000), HiveLoaded: true}}
		mode := e.Mode
		m := o.gen(RoleManager, e.Account, mode, 15, -1, 0).PID
		if mode == ModeS4U {
			o.gen(RoleWorkload, e.Account, mode, 16, -1, 0).ParentPID = m
		}
		return o.ObserverReport
	case "H04":
		// The account's WTS logon starts a session manager beside the
		// headless one, which stays the same process.
		o := newObserved(e.Phase, 60)
		o.stable(peerOf(e.Account))
		o.stable(e.Account)
		sid := roleSID[e.Account]
		o.gen(RoleManager, e.Account, ModeWTS, 10, 40, 0)
		o.Sessions = []SessionSample{{At: o.Started}, {At: ft(10), Users: []SessionUser{{Session: 2, SID: sid, State: 0}}}, {At: ft(41)}}
		o.Logons = []LogonFact{{ID: "00000000:00020000", SID: sid, Type: logonInteractive, LogonTime: ft(9), Source: "audit", Process: "User32"}}
		return o.ObserverReport
	case "H05":
		o := newObserved(e.Phase, 90)
		o.stable(peerOf(e.Account))
		m := o.gen(RoleManager, e.Account, e.Mode, -60, 10.5, 0).PID
		o.gen(RoleWorkload, e.Account, e.Mode, -59, 10.2, 1).ParentPID = m
		o.Marks = []Mark{{Name: "quiet", At: ft(10)}}
		o.Profiles = map[string]ProfileFacts{e.Account: {Registered: true, DirectoryCreated: ft(-90000)}}
		return o.ObserverReport
	case "H07":
		o := newObserved(e.Phase, 30)
		o.stable(peerOf(e.Account))
		m := o.gen(RoleManager, e.Account, e.Mode, -60, -1, 0).PID
		w := o.gen(RoleWorkload, e.Account, e.Mode, -59, 5.2, 0)
		w.ParentPID = m
		crash(w)
		o.gen(RoleWorkload, e.Account, e.Mode, 7, -1, 0).ParentPID = m
		return o.ObserverReport
	case "H08":
		o := newObserved(e.Phase, 30)
		o.stable(peerOf(e.Account))
		m := o.gen(RoleManager, e.Account, e.Mode, -60, 5.1, 0)
		crash(m)
		mpid := m.PID
		o.gen(RoleWorkload, e.Account, e.Mode, -59, 5.3, 1).ParentPID = mpid
		m2 := o.gen(RoleManager, e.Account, e.Mode, 6.5, -1, 0).PID
		o.gen(RoleWorkload, e.Account, e.Mode, 7, -1, 0).ParentPID = m2
		return o.ObserverReport
	case "H09":
		o := newObserved(e.Phase, 60)
		crash(o.gen(RoleBroker, "", ModeSystem, -120, 5.1, 0))
		for _, acct := range []string{AccountA, AccountB} {
			m := o.gen(RoleManager, acct, ModeS4U, -60, 5.2, 1).PID
			o.gen(RoleWorkload, acct, ModeS4U, -59, 5.3, 1).ParentPID = m
		}
		o.gen(RoleBroker, "", ModeSystem, 20, -1, 0)
		for _, acct := range []string{AccountA, AccountB} {
			m := o.gen(RoleManager, acct, ModeS4U, 21, -1, 0).PID
			o.gen(RoleWorkload, acct, ModeS4U, 22, -1, 0).ParentPID = m
		}
		return o.ObserverReport
	}
	return nil
}

// evidenceFor gives each case realistic raw evidence that meets it.
// protectedFor is the product's protected daemon-log DACL for sid.
func protectedFor(sid string) string {
	if sid == SystemSID {
		return "D:P(A;;FA;;;SY)(A;;FA;;;BA)"
	}
	return "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;" + sid + ")"
}

const (
	paddedSHA = "9added0000000000000000000000000000000000000000000000000000000000"
	freshSHA  = "c0ffee0000000000000000000000000000000000000000000000000000000000"
)

// incarnationOf is a distinct process incarnation for a name.
func incarnationOf(name string) (uint32, uint64) {
	h := fnv.New32a()
	h.Write([]byte(name))
	n := h.Sum32()
	return 2000 + n%60000, ft(-100) - uint64(n%100000)*1e4
}

// inventoryFor is the final inventory after every case: the baseline
// receipt the first-use check hashed, nothing left besides the service's
// process, and the baseline's machine state.
func inventoryFor() *InventoryProof {
	facts := func() MachineFacts {
		return MachineFacts{
			Service:   ServiceFacts{Installed: true, StartType: 2, BinaryPath: strings.Repeat("b", 64), Recovery: "1/60000;1/60000;0/0;reset/86400"},
			DataDir:   &ObjectFacts{Owner: SystemSID, DACL: "D:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;0x1200a9;;;BU)"},
			Linger:    &ObjectFacts{Owner: SystemSID, DACL: "D:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"},
			Admission: &ObjectFacts{Owner: "S-1-5-32-544", DACL: "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FR;;;BU)", Size: 48, SHA256: freshSHA},
		}
	}
	scope := func() InventoryScope {
		return InventoryScope{Accounts: []string{sidA, sidB, sidAdmin}, Images: []string{daemonImage, workloadImage, "runtime.test.exe", "journal.test.exe", "other.exe"},
			Resources: slices.Clone(InventoryResources)}
	}
	return &InventoryProof{
		Baseline: InventoryBaseline{Name: "c5-baseline", At: ft(-7100), Boot: firstUseBoot,
			Scope: InventoryScope{Accounts: []string{sidA, sidB, sidAdmin}, Resources: slices.Clone(MachineResources)}, Facts: facts()},
		BaselineSHA256: baselineSHA, At: ft(90000), Boot: testBoot(5), Scope: scope(),
		Broker: &InventoryProcess{Image: daemonImage, SID: SystemSID, PID: 603}, Facts: facts(),
	}
}

const baselineSHA = "ba5e000000000000000000000000000000000000000000000000000000000000"

// profileOf is an account's profile directory.
func profileOf(account string) string { return `C:\Users\wu-` + strings.ToLower(account) }

// daemonLogFor is a manager's diagnostics around the intervention at 10 s.
func daemonLogFor(e Entry) *DaemonLogProof {
	sid := roleSID[e.Account]
	if e.Mode == ModeSystem {
		sid = SystemSID
	}
	obj := func(size int64, sha string) *ObjectFacts {
		return &ObjectFacts{Owner: sid, DACL: protectedFor(sid), Size: size, SHA256: sha}
	}
	after := DaemonLogFacts{At: ft(40), Dir: obj(0, ""), Current: obj(300, freshSHA), Tail: []LogRecord{{Code: daemonOpenCode, At: ft(15.5)}}}
	p := &DaemonLogProof{SID: sid, Root: profileOf(e.Account) + `\AppData\Local\winunitd`, After: after}
	if e.Mode == ModeSystem {
		p.Root = `C:\ProgramData\winunitd`
	}
	switch e.Check {
	case CheckRotation:
		p.Before = &DaemonLogFacts{At: ft(10), Dir: obj(0, ""), Current: obj(RotationBytes+100, paddedSHA)}
		p.After.Archive = obj(RotationBytes+100, paddedSHA)
	case CheckRepair:
		p.Before = &DaemonLogFacts{At: ft(10), Dir: &ObjectFacts{Owner: sid, DACL: "D:AI(A;OICI;FA;;;" + sid + ")(A;OICIID;FA;;;BU)"}}
	}
	return p
}

func evidenceFor(e Entry) Evidence {
	var ev Evidence
	if e.Proof == ProofObserver {
		ev.Observer = observerFor(e)
	}
	if e.Proof == ProofDaemonLog {
		ev.DaemonLog = daemonLogFor(e)
		if e.Observe != nil {
			ev.Observer = observerFor(e)
		}
	}
	if e.Proof == ProofSessionProbe {
		f := tokenFacts(e.Account, e.Mode)
		ev.Token = &TokenProbe{PID: 4100, Created: ft(1), SID: f.SID, Source: f.Source, Session: f.Session, LogonType: f.LogonType, AuthPackage: f.AuthPackage,
			AuthenticationID: f.AuthenticationID, ElevationType: f.ElevationType, Integrity: "medium"}
		ev.Paths = []PathResult{{Probe: "own-log", OK: true}, {Probe: "peer-directory", Win32: errAccessDenied}, {Probe: "peer-log", Win32: errAccessDenied},
			{Probe: "system-directory", Win32: errAccessDenied}, {Probe: "system-log", Win32: errAccessDenied}}
	}
	if e.Proof == ProofInventory {
		ev.Inventory = inventoryFor()
	}
	var p probed
	if e.Proof == ProofProbe {
		p = probeReport(e)
		ev.Observer, ev.Token = p.report, tokenProbeOf(p.probe)
	}
	if e.Proof == ProofNamedTest {
		// One recorder per execution, or per repetition where a case
		// declares runners, and its own test process for every record.
		group := e.Key
		if e.Repetition != "" {
			group = e.Case + "/" + e.Repetition
		}
		token := TokenFacts{SID: SystemSID, AuthenticationID: "00000000:000003e7"}
		if e.Mode == ModeWTS {
			token = tokenFacts(e.Account, ModeWTS)
		}
		rpid, rcreated := incarnationOf("recorder " + group)
		opid, ocreated := incarnationOf("test " + e.Key)
		ev.TestRun = &TestRunProof{Artifact: e.Package + ".test.exe", SHA256: map[string]string{"runtime": testRuntimeSHA, "journal": testJournalSHA}[e.Package],
			Runner: RunnerFacts{PID: rpid, Created: rcreated, Token: token}, Owner: RunnerFacts{PID: opid, Created: max(ocreated, rcreated+1), Token: token}}
		tests := e.Tests
		if len(tests) == 0 {
			tests = []string{e.Test}
		}
		for _, name := range tests {
			ev.TestRun.Events = append(ev.TestRun.Events, TestEvent{Action: "run", Test: name}, TestEvent{Action: "run", Test: name + "/sub"},
				TestEvent{Action: "pass", Test: name + "/sub"}, TestEvent{Action: "pass", Test: name})
		}
		ev.TestRun.Events = append(ev.TestRun.Events, TestEvent{Action: "pass"})
		if e.Mode == ModeS4U {
			o := ev.TestRun.Owner
			ev.TestRun.Subject = &SubjectReport{Test: tests[0] + "/sub", OwnerPID: o.PID, OwnerCreated: o.Created, PID: o.PID + 1, Created: o.Created + 1e7,
				Token: tokenFacts(e.Account, ModeS4U)}
		}
	}
	switch e.Case {
	case "G5":
		ev.Status = &UnitStatusProof{Unit: "failing.service", ActiveState: "active", RestartAttempt: 404, Budget: &StatusBudget{Policy: "always", IntervalSec: 10}, At: ft(700)}
		for _, g := range ev.Observer.Generations {
			if g.Role == RoleWorkload && g.Exited == 0 {
				ev.Status.MainPID = g.PID
			}
		}
	case "H13":
		ev.Paths = []PathResult{{Probe: "unit-fixture", OK: true}, {Probe: "state-write", OK: true}, {Probe: "state-read", OK: true},
			{Probe: "hkcu", OK: true}, {Probe: "known-folders", OK: true}, {Probe: "peer-root", Win32: errAccessDenied}}
	case "H14":
		ev.Paths = []PathResult{{Probe: "absent", Win32: errPathNotFound}, {Probe: "denied", Win32: errAccessDenied}}
	case "H15":
		ok := func(c string, g Generation) PipeResult {
			return PipeResult{Client: c, Connected: true, PID: g.PID, Created: g.Created, Accepted: true, Reason: ReasonAccepted, Held: true}
		}
		no := func(c string, pid uint32, created uint64, reason string) PipeResult {
			return PipeResult{Client: c, Connected: true, PID: pid, Created: created, Reason: reason, Held: true}
		}
		ev.Pipe = []PipeResult{ok(ClientInUnit, p.inUnit), ok(ClientOutsideUnit, p.outside), no(ClientWrongDecision, 5002, ft(6), ReasonAccount),
			{Client: ClientWrongACL, OpenError: errAccessDenied}, no(ClientStaleClaim, 5001, ft(5), ReasonClaim)}
		ev.PipeServers = pipeServers(e, p)
	case "H16":
		for _, c := range []string{ClientSystemOnly, ClientPeerUserPipe, ClientControlPipe, ClientMaintenancePipe} {
			ev.Pipe = append(ev.Pipe, PipeResult{Client: c, OpenError: errAccessDenied, PID: p.inUnit.PID, Created: p.inUnit.Created})
		}
	case "H17":
		ev.TCP = []TCPResult{{Target: "loopback", Connected: true, Sent: 33, Received: 33, Nonce: testNonce, Echoed: true},
			{Target: "peer", Connected: true, Sent: 33, Received: 33, Nonce: testNonce, Echoed: true}}
	case "H18":
		ev.SMB = &SMBResult{Target: testShare, Started: ft(1), Ended: ft(2), Reachable: true, WriteTarget: `\\peer\share\s4u-bare.txt`, Read: OpResult{Op: "read", Win32: errLogonFailure},
			Write: OpResult{Op: "write", Win32: errLogonFailure}, ExpectSHA256: testContent}
	case "H19":
		ev.EFS = &EFSResult{Target: testEFSFile, VolumeEncryption: true, Encrypted: true, Read: OpResult{Op: "read", Win32: errAccessDenied},
			Plain: OpResult{Op: "read", OK: true, SHA256: testContent}, ExpectSHA256: testContent}
	}
	return ev
}

func endpointHealth(account string) []EndpointHealth {
	server := func(pid uint32, sid, image string) EndpointServer {
		return EndpointServer{PID: pid, Created: ft(-100), SID: sid, Image: image}
	}
	h := func(client, pipe string, s EndpointServer) EndpointHealth {
		return EndpointHealth{Client: client, Pipe: pipe, Before: s, After: s}
	}
	return []EndpointHealth{
		h(ClientSystemOnly, QualificationPipePrefix+"system-only", server(601, SystemSID, workloadImage)),
		h(ClientPeerUserPipe, WorkloadPipe(roleSID[peerOf(account)]), server(602, roleSID[peerOf(account)], workloadImage)),
		h(ClientControlPipe, protocol.DefaultPipeName, server(603, SystemSID, daemonImage)),
		h(ClientMaintenancePipe, protocol.MaintenancePipeName, server(603, SystemSID, daemonImage)),
	}
}

func controlEvidence(e Entry, c ControlEntry) Evidence {
	var ev Evidence
	switch c.Name {
	case "first-use":
		ev.FirstUse = &FirstUseProof{SID: roleSID[e.Account], Baseline: "c5-baseline", BaselineSHA256: baselineSHA, Boot: firstUseBoot, At: ft(-3700)}
	case "finite-limit":
		o := newObserved(e.Phase, 30)
		m := o.gen(RoleManager, e.Account, e.Mode, -60, -1, 0).PID
		for i := range 5 {
			o.gen(RoleWorkload, e.Account, e.Mode, float64(i)*1.5, float64(i)*1.5+0.5, 7).ParentPID = m
		}
		ev.Observer = o.ObserverReport
		remaining := 0
		ev.Status = &UnitStatusProof{Unit: "finite.service", ActiveState: "failed", Reason: "start-limit", RestartAttempt: 4,
			Budget: &StatusBudget{Policy: "always", IntervalSec: 10, Burst: 5, Remaining: &remaining}, At: ft(25)}
	case "restore":
		p := probeReport(e)
		ev.Observer, ev.Token = p.report, tokenProbeOf(p.probe)
		ev.Paths = []PathResult{{Probe: "unit-fixture", OK: true}, {Probe: "state-write", OK: true}, {Probe: "state-read", OK: true},
			{Probe: "hkcu", OK: true}, {Probe: "known-folders", OK: true}, {Probe: "peer-root", Win32: errAccessDenied}}
	case "peer-receipt":
		ev.Receipt = &EchoReceipt{Nonce: testNonce, Received: true}
	case "pipe-health":
		ev.Endpoints = endpointHealth(e.Account)
	case "password-share":
		ev.Token = passwordProbe(e.Account)
		ev.SMB = &SMBResult{Target: testShare, Started: ft(1000), Ended: ft(1001), Reachable: true, WriteTarget: `\\peer\share\s4u-control.txt`, Read: OpResult{Op: "read", OK: true, SHA256: testContent},
			Write: OpResult{Op: "write", OK: true}, ExpectSHA256: testContent}
	case "password-decrypt":
		ev.Token = passwordProbe(e.Account)
		ev.EFS = &EFSResult{Target: testEFSFile, VolumeEncryption: true, Encrypted: true, Read: OpResult{Op: "read", OK: true, SHA256: testContent},
			Plain: OpResult{Op: "read", OK: true, SHA256: testContent}, ExpectSHA256: testContent}
	case "server-principal":
		ev.Server = &Principal{Class: "none"}
	}
	return ev
}

// allPassing is a complete passing record set in a valid run order.
func allPassing(t *testing.T, m *Matrix) []Record {
	t.Helper()
	run := testRun(t)
	type item struct {
		key   string
		phase int
		order int
		rec   Record
	}
	var items []item
	for _, e := range m.Expand() {
		if e.Plane == PlaneReference {
			continue
		}
		r := Record{Schema: RecordSchema, Key: e.Key, Kind: KindPrimary, Result: ResultPass, Source: run.Manifest.Source,
			Admission: run.Hash, Executable: run.Manifest.Artifacts[0].SHA256, Matrix: MatrixHash(), Token: tokenFor(e.Account, e.Mode),
			ExecutionID: "x-" + strings.NewReplacer("/", "-", "#", "-").Replace(e.Key), CleanupConfirmed: true, Evidence: evidenceFor(e)}
		if e.Execution != "" {
			r.ExecutionID = "shared-" + e.Execution
		}
		if e.Plane == PlaneOwnerTest {
			r.RunnerID = "runner-" + e.Variant
			if e.Repetition != "" {
				r.RunnerID = "runner-" + e.Repetition
			}
			if r.Evidence.TestRun != nil {
				r.Evidence.TestRun.Runner.ID = r.RunnerID
			}
		}
		order := 1
		if e.Case == "H01" {
			order = 0
		}
		for _, c := range e.Controls {
			r.Controls = append(r.Controls, c.Key)
			cr := Record{Schema: RecordSchema, Key: c.Key, Kind: KindControl, Result: ResultPass, Source: run.Manifest.Source,
				Admission: run.Hash, Executable: r.Executable, Matrix: MatrixHash(), Token: tokenFor(c.Account, c.Mode),
				ExecutionID: "c-" + strings.NewReplacer("/", "-", "#", "-").Replace(c.Key), CleanupConfirmed: true, Evidence: controlEvidence(e, c)}
			if c.Mode == ModePassword {
				cr.Token = &Token{SID: roleSID[e.Account], Session: 3, Source: SourcePassword}
			}
			if c.Proof == ProofObserver || c.Proof == ProofProbe {
				cr.Token = tokenFor(e.Account, e.Mode)
			}
			corder := 2
			if c.ImmediatelyBefore {
				corder = -1
			}
			items = append(items, item{key: c.Key, phase: c.Phase, order: corder, rec: cr})
		}
		items = append(items, item{key: e.Key, phase: e.Phase, order: order, rec: r})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].phase != items[j].phase {
			return items[i].phase < items[j].phase
		}
		if items[i].order != items[j].order {
			return items[i].order < items[j].order
		}
		return items[i].key < items[j].key
	})
	var out []Record
	for i, it := range items {
		r := it.rec
		r.Sequence = i + 1
		r.BootID = testBoot(it.phase).String()
		if strings.HasSuffix(r.Key, "#first-use") {
			r.BootID = firstUseBoot.String()
		}
		if it.phase > 2 {
			r.PasswordLogons = 1
		}
		out = append(out, r)
	}
	return out
}

func findRecord(t *testing.T, rs []Record, key string) int {
	t.Helper()
	for i, r := range rs {
		if r.Key == key {
			return i
		}
	}
	t.Fatalf("no record %s", key)
	return -1
}

// cloneRecords deep-copies the parts mutations change.
func cloneRecords(rs []Record) []Record {
	out := make([]Record, len(rs))
	for i, r := range rs {
		if r.Token != nil {
			tok := *r.Token
			r.Token = &tok
		}
		r.Controls = append([]string(nil), r.Controls...)
		data, _ := json.Marshal(r.Evidence)
		r.Evidence = Evidence{}
		_ = json.Unmarshal(data, &r.Evidence)
		out[i] = r
	}
	return out
}
