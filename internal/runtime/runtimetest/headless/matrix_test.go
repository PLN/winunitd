package headless

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestCaseMatrixCounts(t *testing.T) {
	m := testMatrix(t)
	c := m.Counts()
	// 23 #254 group records, 8 B records and 55 H records
	// (2 + 30 + 2 + 1 + 15 + 4 + 1).
	if c[LedgerC3] != 23 || c[LedgerB] != 8 || c[LedgerH] != 55 || c["total"] != 86 {
		t.Fatalf("counts %v", c)
	}
	byKey := map[string]Entry{}
	controls := 0
	for _, e := range m.Expand() {
		byKey[e.Key] = e
		controls += len(e.Controls)
	}
	if controls != 14 {
		t.Fatalf("controls %d", controls)
	}
	resolved, err := resolveAll(m.Expand())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"B01/A": {"G2/A"}, "B02/B": {"G3/s4u-B"}, "B03/A": {"G4/linger-A"}, "B04/B": {"G4/stop-B"},
		"H10/A": {"G2/A", "G3/s4u-A"}, "H11/B": {"G5/B"}, "G4/crash": {"H09/A", "H09/B"}, "G6/headless-A": {"H12/A"},
	}
	for key, refs := range want {
		if !slices.Equal(resolved[key], refs) {
			t.Errorf("%s resolves to %v, want %v", key, resolved[key], refs)
		}
	}
	for _, shared := range [][]string{{"H01/A", "H02/B"}, {"H03/A", "H03/B"}, {"H09/A", "H09/B"}} {
		if byKey[shared[0]].Execution == "" || byKey[shared[0]].Execution != byKey[shared[1]].Execution {
			t.Errorf("%v do not share an execution", shared)
		}
	}
	// H21's sensitivity control runs with the runner's own SYSTEM token.
	if e := byKey["H21/sensitivity-A"]; e.Mode != ModeSystem || e.Account != AccountSystem || e.Plane != PlaneOwnerTest {
		t.Fatalf("sensitivity entry %+v", e)
	}
	// The SMB and EFS password controls run after the fresh-boot phase.
	for _, key := range []string{"H18/A", "H19/B"} {
		e := byKey[key]
		if e.Phase != 1 {
			t.Errorf("%s phase %d", key, e.Phase)
		}
		for _, c := range e.Controls {
			if c.Mode == ModePassword && c.Phase <= e.Phase {
				t.Errorf("%s control %s in phase %d", key, c.Name, c.Phase)
			}
		}
	}
	if len(m.Select(Selection{Cases: []string{"H20"}})) != 15 {
		t.Fatal("H20 is not three cases in five runners")
	}
}

func TestDecodeMatrixRejectsInvalidTables(t *testing.T) {
	var base map[string]any
	if err := json.Unmarshal(matrixJSON, &base); err != nil {
		t.Fatal(err)
	}
	encode := func(v any) []byte {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	fresh := func() map[string]any {
		var m map[string]any
		_ = json.Unmarshal(encode(base), &m)
		return m
	}
	cases := func(m map[string]any) map[string]any { return m["cases"].(map[string]any) }
	variant := func(m map[string]any, id string, i int) map[string]any {
		return cases(m)[id].(map[string]any)["variants"].([]any)[i].(map[string]any)
	}
	mutations := map[string]func(map[string]any){
		"version":       func(m map[string]any) { m["version"] = 2 },
		"unknown field": func(m map[string]any) { m["extra"] = true },
		"phase gap":     func(m map[string]any) { m["phases"] = m["phases"].([]any)[1:] },
		"case id":       func(m map[string]any) { cases(m)["H23"] = cases(m)["H22"] },
		"ledger":        func(m map[string]any) { cases(m)["H07"].(map[string]any)["ledger"] = "C3" },
		"account mode":  func(m map[string]any) { variant(m, "H07", 0)["mode"] = ModeSystem },
		"plane":         func(m map[string]any) { cases(m)["H07"].(map[string]any)["plane"] = "remote" },
		"phase":         func(m map[string]any) { cases(m)["H07"].(map[string]any)["phase"] = 9 },
		"metric": func(m map[string]any) {
			cases(m)["H07"].(map[string]any)["requires"] = []any{map[string]any{"metric": "speed", "min": 1}}
		},
		"unbounded metric": func(m map[string]any) {
			cases(m)["H07"].(map[string]any)["requires"] = []any{map[string]any{"metric": "durationSec"}}
		},
		"lifecycle without observer": func(m map[string]any) { delete(cases(m)["G2"].(map[string]any), "observe") },
		"replacement without crash": func(m map[string]any) {
			delete(cases(m)["H07"].(map[string]any)["observe"].(map[string]any), "crash")
		},
		"crash outside old roles": func(m map[string]any) {
			cases(m)["H08"].(map[string]any)["observe"].(map[string]any)["old"] = []any{"workload"}
		},
		"child as replacement": func(m map[string]any) { cases(m)["H07"].(map[string]any)["observe"].(map[string]any)["new"] = "child" },
		"unknown observed role": func(m map[string]any) {
			cases(m)["G5"].(map[string]any)["observe"].(map[string]any)["role"] = "service"
		},
		"negative without role": func(m map[string]any) {
			delete(cases(m)["G4"].(map[string]any)["observe"].(map[string]any), "role")
		},
		"peer metric without peer": func(m map[string]any) {
			delete(cases(m)["H07"].(map[string]any)["observe"].(map[string]any), "peer")
		},
		"kept metric without kept": func(m map[string]any) {
			delete(cases(m)["H07"].(map[string]any)["observe"].(map[string]any), "kept")
		},
		"observed owner test": func(m map[string]any) { variant(m, "G5", 0)["plane"] = PlaneOwnerTest },
		"observed field":      func(m map[string]any) { cases(m)["G5"].(map[string]any)["observe"].(map[string]any)["extra"] = 1 },
		"no proof":            func(m map[string]any) { delete(cases(m)["H13"].(map[string]any), "proof") },
		"unknown proof":       func(m map[string]any) { cases(m)["H13"].(map[string]any)["proof"] = "claim" },
		"probe without subject": func(m map[string]any) {
			delete(cases(m)["H13"].(map[string]any)["observe"].(map[string]any), "subject")
		},
		"observer proof without observer": func(m map[string]any) { delete(cases(m)["H03"].(map[string]any), "observe") },
		"named test without package":      func(m map[string]any) { delete(cases(m)["H21"].(map[string]any), "package") },
		"owner test proven otherwise":     func(m map[string]any) { cases(m)["H21"].(map[string]any)["proof"] = ProofObserver },
		"control without proof": func(m map[string]any) {
			delete(cases(m)["H17"].(map[string]any)["controls"].([]any)[0].(map[string]any), "proof")
		},
		"control with another kind of proof": func(m map[string]any) {
			cases(m)["H17"].(map[string]any)["controls"].([]any)[0].(map[string]any)["proof"] = ProofPassword
		},
		"boot kind":            func(m map[string]any) { cases(m)["H01"].(map[string]any)["observe"].(map[string]any)["boot"] = "warm" },
		"reference with proof": func(m map[string]any) { variant(m, "H10", 0)["proof"] = ProofPending },
		"unknown reference":    func(m map[string]any) { variant(m, "B01", 0)["refs"] = []any{"G2/C"} },
		"executed B record": func(m map[string]any) {
			cases(m)["B01"].(map[string]any)["variants"] = []any{map[string]any{"id": "A", "account": "A", "mode": "s4u"}}
		},
		"reference with run": func(m map[string]any) { variant(m, "H10", 0)["execution"] = "x" },
		"lone shared run":    func(m map[string]any) { variant(m, "H07", 0)["execution"] = "alone" },
		"mixed reference":    func(m map[string]any) { variant(m, "H10", 0)["refs"] = []any{"B01/A", "B02/B"} },
		"self reference":     func(m map[string]any) { variant(m, "H10", 0)["refs"] = []any{"H10/A"} },
		"runner repetitions": func(m map[string]any) { variant(m, "H20", 0)["repetitions"] = 4 },
		"test on daemon":     func(m map[string]any) { variant(m, "H07", 0)["test"] = "TestX" },
		"control mode": func(m map[string]any) {
			cases(m)["H17"].(map[string]any)["controls"] = []any{map[string]any{"name": "x", "mode": "wts"}}
		},
		"characterize": func(m map[string]any) { cases(m)["H17"].(map[string]any)["characterize"] = "http" },
		"duplicate variant": func(m map[string]any) {
			cases(m)["H07"].(map[string]any)["variants"] = []any{variant(m, "H07", 0), variant(m, "H07", 0)}
		},
	}
	for name, mutate := range mutations {
		m := fresh()
		mutate(m)
		if _, err := DecodeMatrix(encode(m)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	trailing := append(append([]byte(nil), bytes.TrimSpace(matrixJSON)...), ']')
	if _, err := DecodeMatrix(trailing); err == nil {
		t.Error("trailing delimiter accepted")
	}
}

func TestMetrics(t *testing.T) {
	ev := Lifecycle{Attempts: launches(1, 2, 4, 8, 16, 32, 60, 60, 61)}
	got := metrics(ev, nil, 60)
	if got["cappedGaps"] != 3 || got["shortGapsAfterCap"] != 0 || got["durationSec"] != 244 || got["failures"] != 10 {
		t.Fatalf("crash loop metrics %v", got)
	}
	ev.Attempts = append(ev.Attempts, Attempt{Launched: ev.Attempts[9].Launched.Add(2e9)})
	if got := metrics(ev, nil, 60); got["shortGapsAfterCap"] != 1 || got["failures"] != 10 {
		t.Fatalf("reset after the cap %v", got)
	}
	if got := metrics(Lifecycle{Attempts: launches(1, 1, 1, 1, 1, 1, 1, 1, 20)}, nil, 0); got["maxStartsIn10s"] != 9 {
		t.Fatalf("starts in ten seconds %v", got)
	}
	if got := metrics(Lifecycle{}, nil, 60); len(got) != 0 {
		t.Fatalf("metrics without evidence %v", got)
	}
	w := &Window{Since: at(100), Until: at(250)}
	if got := metrics(Lifecycle{Attempts: launches(1), Negative: w}, nil, 0); got["negativeSec"] != 150 {
		t.Fatalf("negative window %v", got)
	}
	if got := metrics(Lifecycle{Attempts: []Attempt{{Launched: at(250)}}, Negative: w}, nil, 0); got["negativeSec"] != 0 {
		t.Fatalf("launch at the window's end %v", got)
	}
	r := &Replacement{Old: []Process{{PID: 1, Created: 1, Exited: 5}}, New: Process{PID: 2, Created: 5}}
	if got := metrics(Lifecycle{Replacement: r}, nil, 0); got["orderedReplacement"] != 0 {
		t.Fatalf("equal exit and creation time counted as ordered: %v", got)
	}
	r.New.Created = 6
	if got := metrics(Lifecycle{Replacement: r}, nil, 0); got["orderedReplacement"] != 1 {
		t.Fatalf("ordered replacement %v", got)
	}
	if got := metrics(Lifecycle{Replacement: &Replacement{New: Process{PID: 2, Created: 6}}}, nil, 0); got["orderedReplacement"] != 0 {
		t.Fatalf("replacement without old processes %v", got)
	}
}

func TestDecide(t *testing.T) {
	good := CallerObservation{PID: 10, Created: 100, ProcessSID: sidA, ImpersonationSID: sidA}
	if ok, reason := Decide(sidA, good); !ok || reason != ReasonAccepted {
		t.Fatalf("same account: %t %s", ok, reason)
	}
	claimed := good
	claimed.Claim = &Claim{PID: 10, Created: 100, SID: sidA}
	if ok, _ := Decide(sidA, claimed); !ok {
		t.Fatal("matching claim refused")
	}
	cases := map[string]struct {
		mutate func(*CallerObservation)
		want   string
	}{
		"wrong account":         {func(o *CallerObservation) { o.ProcessSID, o.ImpersonationSID = sidB, sidB }, ReasonAccount},
		"unopenable":            {func(o *CallerObservation) { o.OpenError, o.Created, o.ProcessSID = 5, 0, "" }, ReasonOpen},
		"no pid":                {func(o *CallerObservation) { o.PID = 0 }, ReasonOpen},
		"no creation time":      {func(o *CallerObservation) { o.Created = 0 }, ReasonOpen},
		"exited":                {func(o *CallerObservation) { o.Exited = true }, ReasonExited},
		"no impersonation":      {func(o *CallerObservation) { o.ImpersonationError, o.ImpersonationSID = 1346, "" }, ReasonImpersonation},
		"tokens disagree":       {func(o *CallerObservation) { o.ImpersonationSID = sidB }, ReasonImpersonationMismatch},
		"stale claim":           {func(o *CallerObservation) { o.Claim = &Claim{PID: 10, Created: 101} }, ReasonClaim},
		"other pid claimed":     {func(o *CallerObservation) { o.Claim = &Claim{PID: 11, Created: 100} }, ReasonClaim},
		"other account claimed": {func(o *CallerObservation) { o.Claim = &Claim{PID: 10, Created: 100, SID: sidB} }, ReasonClaim},
		"unreadable claim":      {func(o *CallerObservation) { o.Claim = &Claim{} }, ReasonClaim},
		"no request":            {func(o *CallerObservation) { o.RequestError = "missing" }, ReasonRequest},
		// A malformed request is refused before its impersonation is used.
		"malformed request": {func(o *CallerObservation) { o.RequestError, o.ImpersonationSID = "malformed", "" }, ReasonRequest},
	}
	for name, c := range cases {
		o := good
		c.mutate(&o)
		if ok, reason := Decide(sidA, o); ok || reason != c.want {
			t.Errorf("%s: %t %s, want %s", name, ok, reason, c.want)
		}
	}
	// Only the allowed account: the decision does not care which unit or
	// launch the caller belongs to (Model A).
	if ok, _ := Decide(sidB, good); ok {
		t.Fatal("a caller accepted for another allowed account")
	}
}

func TestCharacterize(t *testing.T) {
	loop := TCPResult{Target: "loopback", Connected: true, Echoed: true, Nonce: testNonce}
	peer := TCPResult{Target: "peer", Connected: true, Echoed: true, Nonce: testNonce}
	receipt := &EchoReceipt{Nonce: testNonce, Received: true}
	tcp := []struct {
		name    string
		results []TCPResult
		receipt *EchoReceipt
		want    string
	}{
		{"both ends", []TCPResult{loop, peer}, receipt, Succeeded},
		{"no receipt", []TCPResult{loop, peer}, nil, Inconclusive},
		{"other nonce received", []TCPResult{loop, peer}, &EchoReceipt{Nonce: "x", Received: true}, Inconclusive},
		{"peer refused", []TCPResult{loop, {Target: "peer", Error: wsaEConnRefused}}, receipt, Inconclusive},
		{"loopback failed", []TCPResult{{Target: "loopback", Error: wsaEConnRefused}, peer}, receipt, Failed},
		{"no loopback", []TCPResult{peer}, receipt, Failed},
	}
	for _, c := range tcp {
		if got, why := CharacterizeTCP(c.results, c.receipt); got != c.want {
			t.Errorf("tcp %s: %s (%s), want %s", c.name, got, why, c.want)
		}
	}
	denied := &SMBResult{Target: testShare, Started: ft(1), Ended: ft(2), Reachable: true, Read: OpResult{Win32: errAccessDenied}, ExpectSHA256: testContent}
	ok := &SMBResult{Target: testShare, Started: ft(1), Ended: ft(2), Reachable: true, Read: OpResult{OK: true, SHA256: testContent}, Write: OpResult{OK: true}, ExpectSHA256: testContent}
	control := &SMBResult{Target: testShare, Reachable: true, Read: OpResult{OK: true, SHA256: testContent}, ExpectSHA256: testContent}
	other := *control
	other.Target = `\\peer\other\nonce.txt`
	wrongFile := *control
	wrongFile.Read.SHA256 = strings.Repeat("9", 64)
	failedControl := *control
	failedControl.Read = OpResult{Win32: errAccessDenied}
	account := &Principal{Class: "account", SID: sidA, Target: testShare, At: ft(1.5)}
	attributed := func(f func(*Principal)) *Principal { p := *account; f(&p); return &p }
	smb := []struct {
		name     string
		r        *SMBResult
		password *SMBResult
		server   *Principal
		want     string
	}{
		{"refused with control", denied, control, nil, Refused},
		{"refused without control", denied, nil, nil, Inconclusive},
		{"control on another share", denied, &other, nil, Inconclusive},
		{"control read another file", denied, &wrongFile, nil, Inconclusive},
		{"control failed", denied, &failedControl, nil, Inconclusive},
		{"logon failure", &SMBResult{Target: testShare, Reachable: true, Read: OpResult{Win32: errLogonFailure}, ExpectSHA256: testContent}, control, nil, Refused},
		{"no logon session", &SMBResult{Target: testShare, Reachable: true, Read: OpResult{Win32: errNoSuchLogonSession}, ExpectSHA256: testContent}, control, nil, Refused},
		{"unreachable", &SMBResult{ReachError: wsaETimedOut}, control, nil, Inconclusive},
		{"bad path", &SMBResult{Target: testShare, Reachable: true, Read: OpResult{Win32: errBadNetPath}}, control, nil, Inconclusive},
		{"read works, write denied", &SMBResult{Target: testShare, Reachable: true, Read: OpResult{OK: true}, Write: OpResult{Win32: errAccessDenied}, ExpectSHA256: testContent}, control, nil, Refused},
		{"success as the account", ok, control, account, Succeeded},
		{"success as another account", ok, control, attributed(func(p *Principal) { p.SID = sidB }), Inconclusive},
		{"success on another file", ok, control, attributed(func(p *Principal) { p.Target = other.Target }), Inconclusive},
		{"success at another time", ok, control, attributed(func(p *Principal) { p.At = ft(30) }), Inconclusive},
		{"success as guest", ok, control, &Principal{Class: "guest"}, Inconclusive},
		{"success as machine", ok, control, &Principal{Class: "machine", SID: sidA, Target: testShare, At: ft(1.5)}, Inconclusive},
		{"success unattributed", ok, control, nil, Inconclusive},
		{"wrong content", &SMBResult{Reachable: true, Read: OpResult{OK: true, SHA256: "x"}, Write: OpResult{OK: true}, ExpectSHA256: testContent}, control, nil, Failed},
		{"no probe", nil, control, nil, Failed},
	}
	for _, c := range smb {
		if got, why := CharacterizeSMB(c.r, c.password, c.server, sidA); got != c.want {
			t.Errorf("smb %s: %s (%s), want %s", c.name, got, why, c.want)
		}
	}
	base := EFSResult{Target: testEFSFile, VolumeEncryption: true, Encrypted: true, Read: OpResult{Win32: errAccessDenied}, Plain: OpResult{OK: true}, ExpectSHA256: testContent}
	mod := func(f func(*EFSResult)) *EFSResult { r := base; f(&r); return &r }
	decrypted := mod(func(r *EFSResult) { r.Read = OpResult{OK: true, SHA256: testContent} })
	efs := []struct {
		name     string
		r        *EFSResult
		password *EFSResult
		want     string
	}{
		{"refused with control", &base, decrypted, Refused},
		{"decryption failed", mod(func(r *EFSResult) { r.Read.Win32 = errDecryptionFailed }), decrypted, Refused},
		{"no user keys", mod(func(r *EFSResult) { r.Read.Win32 = errNoUserKeys }), decrypted, Refused},
		{"refused without control", &base, nil, Inconclusive},
		{"control decrypted another file", &base, mod(func(r *EFSResult) { r.Target, r.Read = `C:\\other.txt`, OpResult{OK: true, SHA256: testContent} }), Inconclusive},
		{"control read other plaintext", &base, mod(func(r *EFSResult) { r.Read = OpResult{OK: true, SHA256: "x"} }), Inconclusive},
		{"control failed", &base, &base, Inconclusive},
		{"volume without EFS", mod(func(r *EFSResult) { r.VolumeEncryption = false }), decrypted, Inconclusive},
		{"not encrypted", mod(func(r *EFSResult) { r.Encrypted = false }), decrypted, Inconclusive},
		{"sibling unreadable", mod(func(r *EFSResult) { r.Plain = OpResult{Win32: errAccessDenied} }), decrypted, Inconclusive},
		{"not found", mod(func(r *EFSResult) { r.Read.Win32 = errFileNotFound }), decrypted, Inconclusive},
		{"plaintext read", decrypted, nil, Succeeded},
		{"wrong plaintext", mod(func(r *EFSResult) { r.Read = OpResult{OK: true, SHA256: "x"} }), decrypted, Failed},
	}
	for _, c := range efs {
		if got, why := CharacterizeEFS(c.r, c.password); got != c.want {
			t.Errorf("efs %s: %s (%s), want %s", c.name, got, why, c.want)
		}
	}
}
