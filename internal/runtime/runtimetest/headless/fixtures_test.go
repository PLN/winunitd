package headless

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testSource  = "0123456789abcdef0123456789abcdef01234567"
	sidA        = "S-1-5-21-1-2-3-1001"
	sidB        = "S-1-5-21-1-2-3-1002"
	sidAdmin    = "S-1-5-21-1-2-3-500"
	sidControl  = "S-1-5-21-9-9-9-1101"
	testNonce   = "00112233445566778899aabbccddeeff"
	testContent = "c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00"
)

var testAdmission = sync.OnceValues(func() ([]byte, error) {
	exe, err := ExecutableSHA256()
	if err != nil {
		return nil, err
	}
	return json.Marshal(Admission{Schema: AdmissionSchema, Source: testSource, Artifacts: []Artifact{{Name: "headless.test", SHA256: exe}}})
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

// evidenceFor gives each case realistic raw evidence that meets it.
func evidenceFor(e Entry) Evidence {
	var ev Evidence
	switch e.Case {
	case "G1", "G2":
		ev.Attempts = launches(1, 2, 4, 8, 16, 32, 60, 61, 60, 62, 60, 60, 63, 60, 60)
	case "G3":
		att := launches(1, 2, 4, 8, 16, 32)
		last := att[len(att)-1].Launched
		stable := Attempt{Launched: last.Add(32 * time.Second), Exited: last.Add(162 * time.Second), ExitCode: 1, PID: 300, Created: 3000}
		r1 := Attempt{Launched: stable.Exited.Add(5 * time.Second), Exited: stable.Exited.Add(5500 * time.Millisecond), ExitCode: 1, PID: 301, Created: 3001}
		r2 := Attempt{Launched: r1.Launched.Add(time.Second), Exited: r1.Launched.Add(1500 * time.Millisecond), ExitCode: 1, PID: 302, Created: 3002}
		ev.Attempts = append(att, stable, r1, r2)
	case "G4":
		ev.Attempts = launches(1, 2, 4, 8, 16, 32, 60)
		end := ev.Attempts[len(ev.Attempts)-1].Launched
		ev.Negative = &Window{Since: end.Add(10 * time.Second), Until: end.Add(140 * time.Second)}
	case "G5":
		var gaps []float64
		for range 9 {
			gaps = append(gaps, 0.3)
		}
		for range 20 {
			gaps = append(gaps, 30)
		}
		ev.Attempts = launches(gaps...)
	case "H07", "H08", "H09":
		ev.Replacement = &Replacement{Old: []Process{{Role: "main", PID: 10, Created: 100, Exited: 200}, {Role: "child", PID: 11, Created: 110, Exited: 205}},
			New: Process{Role: "main", PID: 20, Created: 300}}
	case "H13":
		ev.Paths = []PathResult{{Probe: "unit-fixture", OK: true}, {Probe: "state-write", OK: true}, {Probe: "state-read", OK: true},
			{Probe: "hkcu", OK: true}, {Probe: "known-folders", OK: true}, {Probe: "peer-root", Win32: errAccessDenied}}
	case "H14":
		ev.Paths = []PathResult{{Probe: "absent", Win32: errPathNotFound}, {Probe: "denied", Win32: errAccessDenied}}
	case "H15":
		ok := func(c string) PipeResult {
			return PipeResult{Client: c, Connected: true, Accepted: true, Reason: ReasonAccepted, Held: true}
		}
		no := func(c, reason string) PipeResult {
			return PipeResult{Client: c, Connected: true, Reason: reason, Held: true}
		}
		ev.Pipe = []PipeResult{ok(ClientInUnit), ok(ClientOutsideUnit), no(ClientWrongDecision, ReasonAccount),
			{Client: ClientWrongACL, OpenError: errAccessDenied}, no(ClientExited, ReasonExited), no(ClientStaleClaim, ReasonClaim)}
	case "H16":
		for _, c := range []string{ClientSystemOnly, ClientPeerUserPipe, ClientControlPipe, ClientMaintenancePipe} {
			ev.Pipe = append(ev.Pipe, PipeResult{Client: c, OpenError: errAccessDenied})
		}
	case "H17":
		ev.TCP = []TCPResult{{Target: "loopback", Connected: true, Sent: 33, Received: 33, Nonce: testNonce, Echoed: true},
			{Target: "peer", Connected: true, Sent: 33, Received: 33, Nonce: testNonce, Echoed: true}}
	case "H18":
		ev.SMB = &SMBResult{Reachable: true, Read: OpResult{Op: "read", Win32: errLogonFailure}, Write: OpResult{Op: "write", Win32: errLogonFailure}, ExpectSHA256: testContent}
	case "H19":
		ev.EFS = &EFSResult{VolumeEncryption: true, Encrypted: true, Read: OpResult{Op: "read", Win32: errAccessDenied},
			Plain: OpResult{Op: "read", OK: true, SHA256: testContent}, ExpectSHA256: testContent}
	}
	return ev
}

func controlEvidence(c ControlEntry) Evidence {
	var ev Evidence
	switch c.Name {
	case "finite-limit":
		ev.Attempts = launches(0.2, 0.2, 0.2, 0.2)
		ev.Terminal = "start-limit"
	case "peer-receipt":
		ev.Receipt = &EchoReceipt{Nonce: testNonce, Received: true}
	case "password-share", "password-decrypt", "restore":
		ev.Op = &OpResult{Op: "read", OK: true, SHA256: testContent}
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
		}
		order := 1
		if e.Case == "H01" {
			order = 0
		}
		for _, c := range e.Controls {
			r.Controls = append(r.Controls, c.Key)
			cr := Record{Schema: RecordSchema, Key: c.Key, Kind: KindControl, Result: ResultPass, Source: run.Manifest.Source,
				Admission: run.Hash, Executable: r.Executable, Matrix: MatrixHash(), Token: tokenFor(c.Account, c.Mode),
				ExecutionID: "c-" + strings.NewReplacer("/", "-", "#", "-").Replace(c.Key), CleanupConfirmed: true, Evidence: controlEvidence(c)}
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
		r.BootID = "boot-1"
		if it.phase > 1 {
			r.BootID = "boot-" + string(rune('0'+it.phase))
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
