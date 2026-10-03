package headless

import (
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
		// Metrics recomputed from raw attempts.
		"crash loop too short": func(rs []Record) []Record {
			r := &rs[idx(rs, "G2/A")]
			r.Evidence.Attempts = r.Evidence.Attempts[:10]
			return rs
		},
		"reset after cap": func(rs []Record) []Record {
			r := &rs[idx(rs, "G1/B")]
			r.Evidence.Attempts = append(r.Evidence.Attempts, Attempt{Launched: r.Evidence.Attempts[len(r.Evidence.Attempts)-1].Launched.Add(1e9), ExitCode: 1})
			return rs
		},
		"no reset after stable": func(rs []Record) []Record {
			r := &rs[idx(rs, "G3/s4u-A")]
			n := len(r.Evidence.Attempts)
			r.Evidence.Attempts[n-1].Launched = r.Evidence.Attempts[n-2].Launched.Add(60e9)
			return rs
		},
		"stable too short": func(rs []Record) []Record {
			r := &rs[idx(rs, "G3/wts-B")]
			s := &r.Evidence.Attempts[7]
			s.Exited = s.Launched.Add(90e9)
			return rs
		},
		"relaunch while cancelled": func(rs []Record) []Record {
			r := &rs[idx(rs, "G4/linger-A")]
			r.Evidence.Attempts = append(r.Evidence.Attempts, Attempt{Launched: r.Evidence.Negative.Since.Add(60e9)})
			return rs
		},
		"short negative window": func(rs []Record) []Record {
			r := &rs[idx(rs, "G4/stop-B")]
			r.Evidence.Negative.Until = r.Evidence.Negative.Since.Add(90e9)
			return rs
		},
		"too few unit failures": func(rs []Record) []Record {
			r := &rs[idx(rs, "G5/A")]
			r.Evidence.Attempts = r.Evidence.Attempts[:20]
			return rs
		},
		"never more than five starts in ten seconds": func(rs []Record) []Record {
			r := &rs[idx(rs, "G5/B")]
			for i := range r.Evidence.Attempts {
				r.Evidence.Attempts[i].Launched = at(float64(i) * 2.5)
			}
			return rs
		},
		"replacement before old exit": func(rs []Record) []Record {
			rs[idx(rs, "H08/B")].Evidence.Replacement.Old[1].Exited = 400
			return rs
		},
		"old survivor": func(rs []Record) []Record { rs[idx(rs, "H09/A")].Evidence.Replacement.Old[0].Exited = 0; return rs },
		"no replacement evidence": func(rs []Record) []Record {
			rs[idx(rs, "H07/B")].Evidence.Replacement = nil
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
