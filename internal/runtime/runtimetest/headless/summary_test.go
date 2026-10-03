package headless

import (
	"slices"
	"sort"
	"strings"
	"testing"
)

func TestSummarizeCompleteSet(t *testing.T) {
	m := testMatrix(t)
	run := testRun(t)
	s := Summarize(m, allPassing(t, m), run, Selection{})
	if !s.Complete || s.Passed != 86 || s.Required != 86 || s.Controls != 14 || len(s.Problems) != 0 {
		t.Fatalf("complete set: %+v", s)
	}
	// Shared events count once: 86 records, 15 references, three pairs.
	if s.Executions != 86-15-3 {
		t.Fatalf("executions %d", s.Executions)
	}
	if s.Characterizations["H18/A"] != Refused || s.Characterizations["H19/B"] != Refused || s.Characterizations["H17/B"] != Succeeded {
		t.Fatalf("characterizations %v", s.Characterizations)
	}
}

func TestSummarizeFailsClosed(t *testing.T) {
	m := testMatrix(t)
	run := testRun(t)
	base := allPassing(t, m)
	idx := func(rs []Record, key string) int { return findRecord(t, rs, key) }
	remove := func(rs []Record, key string) []Record {
		i := idx(rs, key)
		return append(rs[:i:i], rs[i+1:]...)
	}
	mutations := map[string]func([]Record) []Record{
		"missing record":     func(rs []Record) []Record { return remove(rs, "H07/A") },
		"duplicate record":   func(rs []Record) []Record { return append(rs, rs[idx(rs, "H07/A")]) },
		"failed record":      func(rs []Record) []Record { rs[idx(rs, "H05/B")].Result = ResultFail; return rs },
		"skipped record":     func(rs []Record) []Record { rs[idx(rs, "H04/B")].Result = ResultSkip; return rs },
		"no cleanup":         func(rs []Record) []Record { rs[idx(rs, "H08/A")].CleanupConfirmed = false; return rs },
		"other admission":    func(rs []Record) []Record { rs[idx(rs, "H13/A")].Admission = strings.Repeat("1", 64); return rs },
		"unadmitted program": func(rs []Record) []Record { rs[idx(rs, "H13/A")].Executable = strings.Repeat("2", 64); return rs },
		"stale matrix":       func(rs []Record) []Record { rs[idx(rs, "H13/A")].Matrix = strings.Repeat("3", 64); return rs },
		"no sequence":        func(rs []Record) []Record { rs[idx(rs, "H13/A")].Sequence = 0; return rs },
		"control as primary": func(rs []Record) []Record { rs[idx(rs, "H14/A#restore")].Kind = KindPrimary; return rs },
		"reference recorded": func(rs []Record) []Record {
			r := rs[idx(rs, "H13/A")]
			r.Key = "H10/A"
			return append(rs, r)
		},
		// Tokens.
		"SYSTEM as A": func(rs []Record) []Record {
			rs[idx(rs, "H07/A")].Token = tokenFor(AccountSystem, ModeSystem)
			return rs
		},
		"elevated S4U":     func(rs []Record) []Record { rs[idx(rs, "H07/A")].Token.Elevated = true; return rs },
		"S4U in a session": func(rs []Record) []Record { rs[idx(rs, "H07/A")].Token.Session = 1; return rs },
		"WTS as S4U":       func(rs []Record) []Record { rs[idx(rs, "H07/A")].Token.Source = SourceWTS; return rs },
		"WTS in session 0": func(rs []Record) []Record { rs[idx(rs, "G1/B")].Token.Session = 0; return rs },
		"A under two SIDs": func(rs []Record) []Record { rs[idx(rs, "H08/A")].Token.SID = "S-1-5-21-1-2-3-1003"; return rs },
		"A and B share":    func(rs []Record) []Record { return setRoleSID(rs, sidB, sidA) },
		"sensitivity as A": func(rs []Record) []Record {
			rs[idx(rs, "H21/sensitivity-A")].Token = tokenFor(AccountA, ModeS4U)
			return rs
		},
		"admin elevated": func(rs []Record) []Record { rs[idx(rs, "G6/filtered-admin")].Token.Elevated = true; return rs },
		"no token":       func(rs []Record) []Record { rs[idx(rs, "H03/B")].Token = nil; return rs },
		"control no token": func(rs []Record) []Record {
			rs[idx(rs, "H16/A#pipe-health")].Token = tokenFor(AccountA, ModeS4U)
			return rs
		},
		"password as S4U":    func(rs []Record) []Record { rs[idx(rs, "H19/B#password-decrypt")].Token.Source = SourceS4U; return rs },
		"decrypt as another": func(rs []Record) []Record { rs[idx(rs, "H19/B#password-decrypt")].Token.SID = sidA; return rs },
		// Controls.
		"control unlinked": func(rs []Record) []Record { rs[idx(rs, "G5/A")].Controls = nil; return rs },
		"control missing":  func(rs []Record) []Record { return remove(rs, "G5/A#finite-limit") },
		"control failed":   func(rs []Record) []Record { rs[idx(rs, "H18/B#password-share")].Result = ResultFail; return rs },
		"control no cleanup": func(rs []Record) []Record {
			rs[idx(rs, "H17/A#peer-receipt")].CleanupConfirmed = false
			return rs
		},
		"finite control continued": func(rs []Record) []Record { rs[idx(rs, "G5/B#finite-limit")].Evidence.Terminal = "active"; return rs },
		"foreign control link": func(rs []Record) []Record {
			rs[idx(rs, "H13/A")].Controls = []string{"H14/A#restore"}
			return rs
		},
		// Characterizations recomputed from raw probes and controls.
		"SMB without password control": func(rs []Record) []Record {
			rs[idx(rs, "H18/A#password-share")].Evidence.Op.OK = false
			return rs
		},
		"SMB unreachable":        func(rs []Record) []Record { rs[idx(rs, "H18/B")].Evidence.SMB.Reachable = false; return rs },
		"SMB name error":         func(rs []Record) []Record { rs[idx(rs, "H18/B")].Evidence.SMB.Read.Win32 = errBadNetName; return rs },
		"SMB guest success":      func(rs []Record) []Record { return smbSuccess(rs, idx(rs, "H18/A"), "guest") },
		"EFS sibling unreadable": func(rs []Record) []Record { rs[idx(rs, "H19/B")].Evidence.EFS.Plain.OK = false; return rs },
		"EFS not encrypted":      func(rs []Record) []Record { rs[idx(rs, "H19/B")].Evidence.EFS.Encrypted = false; return rs },
		"EFS wrong plaintext":    func(rs []Record) []Record { return efsWrongPlaintext(rs, idx(rs, "H19/B")) },
		"TCP without peer receipt": func(rs []Record) []Record {
			rs[idx(rs, "H17/A#peer-receipt")].Evidence.Receipt.Received = false
			return rs
		},
		"TCP loopback failed":       func(rs []Record) []Record { rs[idx(rs, "H17/B")].Evidence.TCP[0].Echoed = false; return rs },
		"TCP peer unreachable":      func(rs []Record) []Record { rs[idx(rs, "H17/B")].Evidence.TCP[1].Connected = false; return rs },
		"pipe outside-unit refused": func(rs []Record) []Record { rs[idx(rs, "H15/A")].Evidence.Pipe[1].Accepted = false; return rs },
		"pipe wrong account accepted": func(rs []Record) []Record {
			p := &rs[idx(rs, "H15/B")].Evidence.Pipe[2]
			p.Accepted, p.Reason = true, ReasonAccepted
			return rs
		},
		"pipe caller not held": func(rs []Record) []Record { rs[idx(rs, "H15/B")].Evidence.Pipe[0].Held = false; return rs },
		"pipe timeout as denial": func(rs []Record) []Record {
			rs[idx(rs, "H16/A")].Evidence.Pipe[0].OpenError = 121
			return rs
		},
		"absent path denied": func(rs []Record) []Record { rs[idx(rs, "H14/B")].Evidence.Paths[0].Win32 = errAccessDenied; return rs },
		"peer root readable": func(rs []Record) []Record {
			rs[idx(rs, "H13/B")].Evidence.Paths[5] = PathResult{Probe: "peer-root", OK: true}
			return rs
		},
		"own state not readable": func(rs []Record) []Record {
			rs[idx(rs, "H13/A")].Evidence.Paths = rs[idx(rs, "H13/A")].Evidence.Paths[1:]
			return rs
		},
		// Order, boots and executions.
		"password control before S4U probes": func(rs []Record) []Record {
			rs[idx(rs, "H18/A#password-share")].Sequence = rs[idx(rs, "H18/A")].Sequence - 1
			return rs
		},
		"WTS case in the fresh boot": func(rs []Record) []Record {
			rs[idx(rs, "H04/A")].Sequence = rs[idx(rs, "H17/A")].Sequence
			rs[idx(rs, "H17/A")].Sequence = 99999
			return rs
		},
		"probes across two boots": func(rs []Record) []Record { rs[idx(rs, "H19/B")].BootID = "boot-0"; return rs },
		"password logon before probes": func(rs []Record) []Record {
			rs[idx(rs, "H18/B")].PasswordLogons = 1
			return rs
		},
		"first-use check not just before": func(rs []Record) []Record {
			// Another A record runs between the check and the cold boot.
			b, h := idx(rs, "H01/A"), idx(rs, "H13/A")
			rs[b].Sequence, rs[h].Sequence = rs[h].Sequence, rs[b].Sequence
			return rs
		},
		"first-use check after boot": func(rs []Record) []Record {
			c, h := idx(rs, "H01/A#first-use"), idx(rs, "H01/A")
			rs[c].Sequence, rs[h].Sequence = rs[h].Sequence, rs[c].Sequence
			return rs
		},
		"cold boot split": func(rs []Record) []Record { rs[idx(rs, "H02/B")].ExecutionID = "another-boot"; return rs },
		"one run counted twice": func(rs []Record) []Record {
			rs[idx(rs, "H08/B")].ExecutionID = rs[idx(rs, "H07/B")].ExecutionID
			return rs
		},
		"duplicate sequence": func(rs []Record) []Record {
			rs[idx(rs, "H08/B")].Sequence = rs[idx(rs, "H07/B")].Sequence
			return rs
		},
		"runner reused": func(rs []Record) []Record {
			for _, v := range []string{"revocation", "shutdown", "deadline"} {
				rs[idx(rs, "H20/"+v+"/r2")].RunnerID = "runner-r1"
			}
			return rs
		},
		"repetition across runners": func(rs []Record) []Record {
			rs[idx(rs, "H20/shutdown/r3")].RunnerID = "runner-x"
			return rs
		},
		"owner test without runner": func(rs []Record) []Record { rs[idx(rs, "H21/security-B")].RunnerID = ""; return rs },
		"daemon record with runner": func(rs []Record) []Record { rs[idx(rs, "H22/all")].RunnerID = "runner"; return rs },
	}
	for name, mutate := range mutations {
		s := Summarize(m, mutate(cloneRecords(base)), run, Selection{})
		if s.Complete {
			t.Errorf("%s: summary complete", name)
		}
	}
	if s := Summarize(m, base, AdmittedRun{}, Selection{}); s.Complete {
		t.Error("complete without an admitted run")
	}
}

// gensOf returns the generations of role and account in creation order.
func gensOf(r *ObserverReport, role, account string) []*Generation {
	var out []*Generation
	for i := range r.Generations {
		if g := &r.Generations[i]; g.Role == role && g.Account == account {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created < out[j].Created })
	return out
}

// Lifecycle metrics come only from the observer's report, and each way the
// report can fall short leaves the summary incomplete for that reason.
func TestSummarizeObserverFailsClosed(t *testing.T) {
	m := testMatrix(t)
	run := testRun(t)
	base := allPassing(t, m)
	report := func(rs []Record, key string) *ObserverReport { return rs[findRecord(t, rs, key)].Evidence.Observer }
	// shared applies fn to the report of every record of a shared execution.
	shared := func(rs []Record, keys []string, fn func(*ObserverReport)) {
		for _, k := range keys {
			fn(report(rs, k))
		}
	}
	shift := func(g *Generation, by float64) {
		d := uint64(by * 1e7)
		g.Created, g.Seen, g.Exited = g.Created+d, g.Seen+d, g.Exited+d
		if g.Crashed != 0 {
			g.Crashed += d
		}
	}
	tests := []struct {
		name, want string
		mutate     func([]Record)
	}{
		{"crash loop too short", "G2/A: durationSec below its minimum", func(rs []Record) {
			r := report(rs, "G2/A")
			keep := gensOf(r, RoleManager, AccountA)[9].Created
			r.Generations = slices.DeleteFunc(r.Generations, func(g Generation) bool { return g.Role == RoleManager && g.Account == AccountA && g.Created > keep })
		}},
		{"reset after cap", "G1/B: shortGapsAfterCap above its maximum", func(rs []Record) {
			r := report(rs, "G1/B")
			gs := gensOf(r, RoleManager, AccountB)
			g := *gs[len(gs)-1]
			g.PID++
			shift(&g, 1)
			r.Generations = append(r.Generations, g)
			r.Ended = max(r.Ended, g.Exited)
		}},
		{"no reset after stable", "G3/s4u-A: postResetGapSec above its maximum", func(rs []Record) {
			gs := gensOf(report(rs, "G3/s4u-A"), RoleManager, AccountA)
			shift(gs[len(gs)-1], 59)
			report(rs, "G3/s4u-A").Ended += 60e7
		}},
		{"stable too short", "G3/wts-B: stableSec below its minimum", func(rs []Record) {
			g := gensOf(report(rs, "G3/wts-B"), RoleManager, AccountB)[7]
			g.Exited = g.Created + 90e7
			g.Crashed = g.Exited - 1000
		}},
		{"relaunch while cancelled", "G4/linger-A: negativeSec below its minimum", func(rs []Record) {
			r := report(rs, "G4/linger-A")
			r.Generations = append(r.Generations, Generation{Role: RoleManager, Account: AccountA, PID: 9, Created: r.Marks[0].At + 60e7,
				Seen: r.Marks[0].At + 61e7, Token: tokenFacts(AccountA, ModeS4U)})
		}},
		{"short negative window", "G4/stop-B: negativeSec below its minimum", func(rs []Record) {
			r := report(rs, "G4/stop-B")
			r.Ended = r.Marks[0].At + 90e7
		}},
		{"no quiet mark", "G4/linger-B: observer: the observer saw no quiet mark", func(rs []Record) { report(rs, "G4/linger-B").Marks = nil }},
		{"too few unit failures", "G5/A: failures below its minimum", func(rs []Record) {
			r := report(rs, "G5/A")
			keep := gensOf(r, RoleWorkload, AccountA)[19].Created
			r.Generations = slices.DeleteFunc(r.Generations, func(g Generation) bool { return g.Role == RoleWorkload && g.Created > keep })
		}},
		{"never more than five starts in ten seconds", "G5/B: maxStartsIn10s below its minimum", func(rs []Record) {
			for i, g := range gensOf(report(rs, "G5/B"), RoleWorkload, AccountB) {
				life := g.Exited - g.Created
				g.Created = ft(float64(i) * 2.5)
				g.Seen, g.Exited = g.Created+5e5, g.Created+life
			}
		}},
		{"short-lived generation", "G5/B: observer: a generation lived too briefly", func(rs []Record) {
			g := gensOf(report(rs, "G5/B"), RoleWorkload, AccountB)[3]
			g.Seen, g.Exited = g.Created+1, g.Created+5e5
		}},
		{"scans too far apart", "G5/A: observer: the observer's scans were too far apart", func(rs []Record) { report(rs, "G5/A").MaxGap = 3e6 }},
		{"replacement before old exit", "H08/B: orderedReplacement below its minimum", func(rs []Record) {
			gensOf(report(rs, "H08/B"), RoleWorkload, AccountB)[0].Exited = ft(8)
		}},
		{"old survivor", "H09/A: orderedReplacement below its minimum", func(rs []Record) {
			shared(rs, []string{"H09/A", "H09/B"}, func(r *ObserverReport) { gensOf(r, RoleWorkload, AccountA)[0].Exited = 0 })
		}},
		{"no broker crash", "H09/B: observer: the observer crashed no broker", func(rs []Record) {
			shared(rs, []string{"H09/A", "H09/B"}, func(r *ObserverReport) { gensOf(r, RoleBroker, "")[0].Crashed = 0 })
		}},
		{"shared execution with two reports", "shared execution broker-crash has two observer reports", func(rs []Record) {
			report(rs, "H09/B").Ended += 1e7
		}},
		{"no workload crash", "H07/B: observer: the observer crashed no workload", func(rs []Record) {
			gensOf(report(rs, "H07/B"), RoleWorkload, AccountB)[0].Crashed = 0
		}},
		{"no observer report", "H07/A: no observer report", func(rs []Record) { rs[findRecord(t, rs, "H07/A")].Evidence.Observer = nil }},
		{"observer outside the run", "H08/A: observer executable outside the admitted run", func(rs []Record) {
			report(rs, "H08/A").Executable = strings.Repeat("4", 64)
		}},
		{"observer on another boot", "H05/A: observer report is from another boot", func(rs []Record) { report(rs, "H05/A").Boot.Counter = 9 }},
		{"unfinished observer", "G2/B: observer: observer stage", func(rs []Record) { report(rs, "G2/B").Stage = ObserverRunning }},
		{"unidentified process", "H07/A: observer: 1 processes of a watched image were not identified", func(rs []Record) {
			r := report(rs, "H07/A")
			r.Unidentified = []Unidentified{{PID: 9, At: r.Started, Win32: 87}}
		}},
		{"manager under a session token", "G2/A: observer: an observed generation did not run under the account's s4u token", func(rs []Record) {
			gensOf(report(rs, "G2/A"), RoleManager, AccountA)[2].Token = tokenFacts(AccountA, ModeWTS)
		}},
		{"replacement under another token source", "H08/A: observer: an observed generation did not run under the account's s4u token", func(rs []Record) {
			gensOf(report(rs, "H08/A"), RoleWorkload, AccountA)[1].Token.Source = "User32"
		}},
		{"observer watched another SID", "H07/B: observer: the observer watched another SID for account B", func(rs []Record) {
			r := report(rs, "H07/B")
			r.Accounts[AccountB] = "S-1-5-21-7-7-7-1002"
			for i := range r.Generations {
				if r.Generations[i].Account == AccountB {
					r.Generations[i].Token.SID = r.Accounts[AccountB]
				}
			}
		}},
		{"peer restarted", "H07/A: peerUnchanged below its minimum", func(rs []Record) {
			gensOf(report(rs, "H07/A"), RoleWorkload, AccountB)[0].Exited = ft(6)
		}},
		{"own manager restarted", "H07/B: kept below its minimum", func(rs []Record) {
			r := report(rs, "H07/B")
			gensOf(r, RoleManager, AccountB)[0].Exited = ft(6)
		}},
		{"not drained", "H05/B: drained below its minimum", func(rs []Record) {
			gensOf(report(rs, "H05/B"), RoleWorkload, AccountB)[0].Exited = 0
		}},
		{"manager after the no-grant reboot", "H03/A: negativeSec below its minimum", func(rs []Record) {
			shared(rs, []string{"H03/A", "H03/B"}, func(r *ObserverReport) {
				r.Generations = append(r.Generations, Generation{Role: RoleManager, Account: AccountA, PID: 9, Created: ft(30), Seen: ft(30.05),
					Token: tokenFacts(AccountA, ModeS4U)})
			})
		}},
		{"crash before it was seen", "H08/A: observer: generation", func(rs []Record) {
			g := gensOf(report(rs, "H08/A"), RoleManager, AccountA)[0]
			g.Crashed = g.Seen - 1
		}},
		{"report on an unobserved case", "H13/A: carries an observer report its case does not use", func(rs []Record) {
			rs[findRecord(t, rs, "H13/A")].Evidence.Observer = report(rs, "H07/A")
		}},
	}
	for _, tc := range tests {
		rs := cloneRecords(base)
		tc.mutate(rs)
		s := Summarize(m, rs, run, Selection{})
		if s.Complete || !strings.Contains(strings.Join(s.Problems, "\n"), tc.want) {
			t.Errorf("%s: complete %t, problems %q", tc.name, s.Complete, s.Problems)
		}
	}
}

func setRoleSID(rs []Record, from, to string) []Record {
	for i := range rs {
		if rs[i].Token != nil && rs[i].Token.SID == from {
			rs[i].Token.SID = to
		}
	}
	return rs
}

func smbSuccess(rs []Record, i int, class string) []Record {
	r := &rs[i]
	r.Evidence.SMB.Read = OpResult{Op: "read", OK: true, SHA256: testContent}
	r.Evidence.SMB.Write = OpResult{Op: "write", OK: true}
	for j := range rs {
		if rs[j].Key == r.Key+"#server-principal" {
			rs[j].Evidence.Server = &Principal{Class: class}
		}
	}
	return rs
}

func efsWrongPlaintext(rs []Record, i int) []Record {
	rs[i].Evidence.EFS.Read = OpResult{Op: "read", OK: true, SHA256: strings.Repeat("9", 64)}
	return rs
}

// A failed referenced record fails every record that cites it, and
// a success under bare S4U is credited only when the server names the
// account.
func TestSummarizeReferencesAndSuccesses(t *testing.T) {
	m := testMatrix(t)
	run := testRun(t)
	rs := cloneRecords(allPassing(t, m))
	rs[findRecord(t, rs, "G2/A")].Result = ResultFail
	s := Summarize(m, rs, run, Selection{Cases: []string{"B01", "H10", "G2"}})
	if s.Passed != 3 || s.Failed != 3 || s.Incomplete != 0 {
		t.Fatalf("failed reference: %+v", s)
	}
	rs = cloneRecords(allPassing(t, m))
	rs = smbSuccess(rs, findRecord(t, rs, "H18/A"), "account")
	rs[findRecord(t, rs, "H18/A#server-principal")].Evidence.Server.Role = AccountA
	s = Summarize(m, rs, run, Selection{})
	if !s.Complete || s.Characterizations["H18/A"] != Succeeded {
		t.Fatalf("attributed success: %+v", s)
	}
	rs[findRecord(t, rs, "H18/A#server-principal")].Evidence.Server.Role = AccountB
	if s := Summarize(m, rs, run, Selection{}); s.Complete || s.Characterizations["H18/A"] != Inconclusive {
		t.Fatalf("success attributed to another account: %+v", s)
	}
}

func TestSummarizeSelectionIsPartial(t *testing.T) {
	m := testMatrix(t)
	run := testRun(t)
	phase1 := map[string]bool{}
	for _, e := range m.Select(Selection{Phases: []int{1}}) {
		phase1[e.Key] = true
	}
	var rs []Record
	for _, r := range allPassing(t, m) {
		if phase1[primaryOf(r.Key)] {
			rs = append(rs, r)
		}
	}
	s := Summarize(m, rs, run, Selection{Phases: []int{1}})
	if s.Complete || !s.Partial || s.Passed != s.Selected || len(s.Problems) != 0 || len(s.Omitted) == 0 {
		t.Fatalf("phase 1 only: %+v", s)
	}
	if s := Summarize(m, rs, run, Selection{}); s.Complete || len(s.Missing) == 0 {
		t.Fatalf("whole matrix with phase 1 only: %+v", s)
	}
}

// The first-use check must be the last thing before the cold boot for that
// account: another of its records in between leaves H01 unproven.
func TestSummarizeFirstUseImmediatelyBefore(t *testing.T) {
	m := testMatrix(t)
	rs := cloneRecords(allPassing(t, m))
	b, h := findRecord(t, rs, "H01/A"), findRecord(t, rs, "H13/A")
	rs[b].Sequence, rs[h].Sequence = rs[h].Sequence, rs[b].Sequence
	s := Summarize(m, rs, testRun(t), Selection{})
	if s.Complete || !strings.Contains(strings.Join(s.Problems, "\n"), "H01/A#first-use: H13/A ran between the control and its record") {
		t.Fatalf("problems %q", s.Problems)
	}
	// Account B's records may run between them.
	rs = cloneRecords(allPassing(t, m))
	b, h = findRecord(t, rs, "H01/A"), findRecord(t, rs, "H02/B")
	rs[b].Sequence, rs[h].Sequence = rs[h].Sequence, rs[b].Sequence
	if s := Summarize(m, rs, testRun(t), Selection{}); !s.Complete {
		t.Fatalf("B between the check and H01: %q", s.Problems)
	}
}
