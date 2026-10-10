package headless

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestFiletimeTime(t *testing.T) {
	if got := FiletimeTime(116444736000000000); !got.Equal(time.Unix(0, 0)) {
		t.Fatalf("epoch %v", got)
	}
	if got := FiletimeTime(ft(2.5)); !got.Equal(at(2.5)) {
		t.Fatalf("t0 plus 2.5 s: %v", got)
	}
}

func TestClassifyToken(t *testing.T) {
	s4u := tokenFacts(AccountA, ModeS4U)
	wts := tokenFacts(AccountB, ModeWTS)
	if ClassifyToken(s4u) != SourceS4U || ClassifyToken(wts) != SourceWTS || ClassifyToken(TokenFacts{SID: SystemSID}) != SourceProcess {
		t.Fatal("genuine tokens misclassified")
	}
	batch := s4u
	batch.LogonType = logonBatch
	if ClassifyToken(batch) != SourceS4U {
		t.Fatal("a batch S4U logon misclassified")
	}
	for name, mutate := range map[string]func(*TokenFacts){
		"S4U in a session":    func(f *TokenFacts) { f.Session = 1 },
		"another source":      func(f *TokenFacts) { f.Source = "User32" },
		"elevated":            func(f *TokenFacts) { f.Elevated = true },
		"interactive logon":   func(f *TokenFacts) { f.LogonType = logonInteractive },
		"service logon":       func(f *TokenFacts) { f.LogonType = 5 },
		"unknown logon type":  func(f *TokenFacts) { f.LogonType = 0 },
		"padded source":       func(f *TokenFacts) { f.Source = productTokenSource + " " },
		"session token as S4": func(f *TokenFacts) { f.Session, f.LogonType = 0, logonRemoteInteractive },
	} {
		f := s4u
		mutate(&f)
		if got := ClassifyToken(f); got == SourceS4U {
			t.Errorf("%s classified as S4U", name)
		}
	}
	elevated := wts
	elevated.Elevated = true
	if ClassifyToken(elevated) != "" {
		t.Error("an elevated session token classified")
	}
	filtered := wts
	filtered.ElevationType = tokenElevationTypeLimit
	if ClassifyToken(filtered) != ClassFilteredAdmin {
		t.Error("an administrator's filtered session token not classified")
	}
	full := wts
	full.ElevationType = 2
	if ClassifyToken(full) != "" {
		t.Error("a fully elevated type classified as a standard session token")
	}
	if tokenClass(ModeFilteredAdmin) != ClassFilteredAdmin || tokenClass(ModeS4U) != SourceS4U || tokenClass(ModeWTS) != SourceWTS {
		t.Error("mode classes")
	}
}

// The native runtime tests write their S4U subject with exactly these
// fields; the receipt decodes them strictly.
func TestSubjectReportDecodes(t *testing.T) {
	report := `{"sid":"S-1-5-21-1-2-3-1001","session":0,"elevated":false,"source":"winunitd","logonType":3,"authPackage":"Kerberos","authenticationId":"00000000:00010000","elevationType":1}`
	var f TokenFacts
	if err := decodeStrict([]byte(report), &f); err != nil || ClassifyToken(f) != SourceS4U {
		t.Fatalf("subject report %+v %v", f, err)
	}
}

func validReport() *ObserverReport {
	o := newObserved(2, 30)
	o.stable(AccountB)
	o.gen(RoleManager, AccountA, ModeS4U, -60, -1, 0)
	crash(o.gen(RoleWorkload, AccountA, ModeS4U, -59, 5.2, 0))
	o.gen(RoleWorkload, AccountA, ModeS4U, 7, -1, 0)
	crash(o.gen(RoleBroker, "", ModeSystem, -120, 8, 0))
	o.Marks = []Mark{{Name: "quiet", At: ft(9)}}
	return o.ObserverReport
}

func TestObserverReportValidate(t *testing.T) {
	if err := validReport().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ObserverReport){
		"schema":                func(r *ObserverReport) { r.Schema = 2 },
		"running":               func(r *ObserverReport) { r.Stage = ObserverRunning },
		"failed":                func(r *ObserverReport) { r.Stage = ObserverFailed },
		"executable":            func(r *ObserverReport) { r.Executable = "abc" },
		"no boot":               func(r *ObserverReport) { r.Boot.Time = 0 },
		"one scan":              func(r *ObserverReport) { r.Scans = 1 },
		"ended before started":  func(r *ObserverReport) { r.Ended = r.Started - 1 },
		"SYSTEM account":        func(r *ObserverReport) { r.Accounts[AccountA] = SystemSID },
		"shared SID":            func(r *ObserverReport) { r.Accounts[AccountB] = r.Accounts[AccountA] },
		"unknown account role":  func(r *ObserverReport) { r.Accounts["C"] = "S-1-5-21-1-2-3-1009" },
		"SYSTEM as admin":       func(r *ObserverReport) { r.Accounts[AccountAdmin] = SystemSID },
		"duplicate generation":  func(r *ObserverReport) { r.Generations = append(r.Generations, r.Generations[0]) },
		"no PID":                func(r *ObserverReport) { r.Generations[0].PID = 0 },
		"seen before created":   func(r *ObserverReport) { r.Generations[3].Seen = r.Generations[3].Created - 1 },
		"seen before the scans": func(r *ObserverReport) { r.Generations[0].Seen = r.Started - 1 },
		"seen after the scans":  func(r *ObserverReport) { r.Generations[4].Seen = r.Ended + 1 },
		"exit before creation":  func(r *ObserverReport) { r.Generations[3].Exited = r.Generations[3].Created - 1 },
		"exit after the scans":  func(r *ObserverReport) { r.Generations[3].Exited = r.Ended + 1 },
		"no final scan":         func(r *ObserverReport) { r.FinalScan = 0 },
		"ended after the final scan": func(r *ObserverReport) {
			r.Ended += 1e7
			r.Sampling.Last = r.Ended
		},
		"no initial session sample": func(r *ObserverReport) { r.Sessions = nil },
		"sampling ended early":      func(r *ObserverReport) { r.Sampling.Last = r.Sampling.First + 1e7 },
		"session after the last sample": func(r *ObserverReport) {
			r.Sessions = append(r.Sessions, SessionSample{At: r.Sampling.Last + 1})
		},
		"crash without exit":   func(r *ObserverReport) { r.Generations[3].Exited = 0 },
		"crash after exit":     func(r *ObserverReport) { r.Generations[3].Crashed = r.Generations[3].Exited + 1 },
		"crash before seen":    func(r *ObserverReport) { r.Generations[3].Crashed = r.Generations[3].Seen - 1 },
		"role":                 func(r *ObserverReport) { r.Generations[0].Role = "service" },
		"account SID mismatch": func(r *ObserverReport) { r.Generations[2].Token.SID = sidB },
		"unwatched account":    func(r *ObserverReport) { r.Generations[2].Account = "C" },
		"broker with account":  func(r *ObserverReport) { r.Generations[5].Account = AccountA },
		"broker not SYSTEM":    func(r *ObserverReport) { r.Generations[5].Token.SID = sidA },
		"mark name":            func(r *ObserverReport) { r.Marks[0].Name = "Quiet!" },
		"start mark":           func(r *ObserverReport) { r.Marks[0].Name = MarkStart },
		"mark outside":         func(r *ObserverReport) { r.Marks[0].At = r.Ended + 1 },
		"release outside":      func(r *ObserverReport) { r.Releases = []Mark{{Name: "release", At: r.Started - 1}} },
		"too many generations": func(r *ObserverReport) {
			for i := range MaxGenerations {
				r.Generations = append(r.Generations, Generation{Role: RoleChild, Account: AccountA, PID: uint32(50000 + i), Created: r.Started,
					Seen: r.Started, Token: TokenFacts{SID: sidA}})
			}
		},
	} {
		r := validReport()
		mutate(r)
		if err := r.Validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// A replacement counts the processes of the old roles alive at the
// observer's first crash and the first new process created after it.
func TestDeriveReplacement(t *testing.T) {
	r := validReport()
	spec := ObserveSpec{Crash: RoleWorkload, Old: []string{RoleWorkload, RoleChild}, New: RoleWorkload, Kept: []string{RoleManager}, Peer: true}
	l, problems := DeriveLifecycle(r, spec, AccountA, sidA, ModeS4U)
	if len(problems) != 0 || l.Replacement == nil || len(l.Replacement.Old) != 1 || l.Replacement.New.Created != ft(7) || !*l.Kept || !*l.Peer {
		t.Fatalf("replacement %+v kept %v peer %v: %q", l.Replacement, l.Kept, l.Peer, problems)
	}
	if got := metrics(l, nil, 0, 0); got["orderedReplacement"] != 1 || got["kept"] != 1 || got["peerUnchanged"] != 1 {
		t.Fatalf("metrics %v", got)
	}
	// A child that exited before the crash is not an old process; one that
	// outlived the new workload is.
	o := &observed{ObserverReport: r, pid: 5000}
	o.gen(RoleChild, AccountA, ModeS4U, 1, 2, 0)
	if l, _ := DeriveLifecycle(r, spec, AccountA, sidA, ModeS4U); len(l.Replacement.Old) != 1 {
		t.Fatalf("an exited child counted as old: %+v", l.Replacement)
	}
	o.gen(RoleChild, AccountA, ModeS4U, 3, 8, 0)
	if l, _ := DeriveLifecycle(r, spec, AccountA, sidA, ModeS4U); len(l.Replacement.Old) != 2 || metrics(l, nil, 0, 0)["orderedReplacement"] != 0 {
		t.Fatalf("a surviving child not counted: %+v", l.Replacement)
	}
	// Another account's crash is not this account's.
	if _, problems := DeriveLifecycle(r, spec, AccountB, sidB, ModeS4U); !slices.Contains(problems, "the observer crashed no workload") {
		t.Fatalf("B credited with A's crash: %q", problems)
	}
	// The broker's crash applies to every account.
	broker := ObserveSpec{Crash: RoleBroker, Old: []string{RoleBroker, RoleManager}, New: RoleWorkload}
	if l, problems := DeriveLifecycle(r, broker, AccountA, sidA, ModeS4U); len(problems) != 0 || len(l.Replacement.Old) != 2 {
		t.Fatalf("broker replacement %+v %q", l.Replacement, problems)
	}
}

func TestDeriveNegativeWindow(t *testing.T) {
	r := validReport()
	l, problems := DeriveLifecycle(r, ObserveSpec{Role: RoleManager, Negative: "quiet"}, AccountA, sidA, ModeS4U)
	if len(problems) != 0 || l.Negative == nil || !l.Negative.Since.Equal(at(9)) || !l.Negative.Until.Equal(at(30)) {
		t.Fatalf("quiet window %+v %q", l.Negative, problems)
	}
	if l, _ := DeriveLifecycle(r, ObserveSpec{Role: RoleManager, Negative: MarkStart}, AccountA, sidA, ModeS4U); !l.Negative.Since.Equal(at(-5)) {
		t.Fatalf("start window %+v", l.Negative)
	}
	if _, problems := DeriveLifecycle(r, ObserveSpec{Role: RoleManager, Negative: "other"}, AccountA, sidA, ModeS4U); len(problems) != 1 || !strings.Contains(problems[0], "no other mark") {
		t.Fatalf("missing mark %q", problems)
	}
	if l, _ := DeriveLifecycle(r, ObserveSpec{Role: RoleManager, Drained: true}, AccountA, sidA, ModeS4U); *l.Drained {
		t.Fatal("a running account counted as drained")
	}
}
