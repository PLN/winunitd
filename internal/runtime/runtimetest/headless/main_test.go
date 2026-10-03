package headless

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func runMain(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := Main(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestMainMatrix(t *testing.T) {
	if code, out, _ := runMain("matrix", "--hash"); code != 0 || strings.TrimSpace(out) != MatrixHash() {
		t.Fatalf("hash: %d %q", code, out)
	}
	code, out, _ := runMain("matrix", "--counts")
	var counts map[string]int
	if code != 0 || json.Unmarshal([]byte(out), &counts) != nil || counts["total"] != 86 {
		t.Fatalf("counts: %d %q", code, out)
	}
	code, out, _ = runMain("matrix", "--phase", "1", "--account", "A")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if code != 0 || len(lines) != 7 { // H01, H13-H18
		t.Fatalf("phase 1 for A: %d %d lines", code, len(lines))
	}
	for _, args := range [][]string{{"matrix", "--ledger", "X"}, {"matrix", "--case", "H99"}, {"matrix", "--phase", "x"}, {"matrix", "extra"}, {"nope"}, {}} {
		if code, _, _ := runMain(args...); code != 2 {
			t.Errorf("%q exit %d, want 2", args, code)
		}
	}
	if runtime.GOOS != "windows" {
		if code, _, errOut := runMain("probe-token", "--out", "/tmp/x.json"); code != 1 || !strings.Contains(errOut, "requires Windows") {
			t.Fatalf("Windows role elsewhere: %d %q", code, errOut)
		}
	}
}

func TestRecordAndSummarizeCommands(t *testing.T) {
	m := testMatrix(t)
	admission := writeTestAdmission(t)
	results := t.TempDir()
	all := allPassing(t, m)
	observedKey := "H07/A"
	write := func(name string, v any) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), name)
		data, _ := json.Marshal(v)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	// The first record and an observed one go through the record command,
	// as a driver would; the observed one with its observer's report.
	first, observed := all[0], all[findRecord(t, all, observedKey)]
	for _, r := range all {
		if r.Key != first.Key && r.Key != observedKey {
			if err := WriteRecord(results, r); err != nil {
				t.Fatal(err)
			}
		}
	}
	observation := func(r Record) Observation {
		return Observation{Key: r.Key, Kind: r.Kind, Result: r.Result, Token: r.Token, ExecutionID: r.ExecutionID, Sequence: r.Sequence,
			BootID: r.BootID, PasswordLogons: r.PasswordLogons, RunnerID: r.RunnerID, CleanupConfirmed: r.CleanupConfirmed,
			Controls: r.Controls, Evidence: r.Evidence}
	}
	reportPath := write("observer.json", observed.Evidence.Observer)
	o := observation(observed)
	if code, _, errOut := runMain("record", "--observation", write("inline.json", o), "--results", results, "--admission", admission); code != 1 || !strings.Contains(errOut, "cannot carry an observer report") {
		t.Fatalf("inline observer report: %d %s", code, errOut)
	}
	o.Evidence.Observer = nil
	observedPath := write("observed.json", o)
	if code, _, errOut := runMain("record", "--observation", observedPath, "--results", results, "--admission", admission); code != 1 || !strings.Contains(errOut, "needs the observer's report") {
		t.Fatalf("observed record without its report: %d %s", code, errOut)
	}
	if code, _, errOut := runMain("record", "--observation", observedPath, "--observer", reportPath, "--results", results, "--admission", admission); code != 0 {
		t.Fatalf("record with observer: %d %s", code, errOut)
	}
	obsPath := write("observation.json", observation(first))
	if code, _, errOut := runMain("record", "--observation", obsPath, "--observer", reportPath, "--results", results, "--admission", admission); code != 1 || !strings.Contains(errOut, "not an observed case") {
		t.Fatalf("observer report on an unobserved record: %d %s", code, errOut)
	}
	if code, _, errOut := runMain("record", "--observation", obsPath, "--results", results, "--admission", admission); code != 0 {
		t.Fatalf("record: %d %s", code, errOut)
	}
	if code, _, _ := runMain("record", "--observation", obsPath, "--results", results, "--admission", admission); code != 1 {
		t.Fatal("a record was replaced")
	}
	code, out, errOut := runMain("summarize", "--results", results, "--admission", admission)
	if code != SummaryComplete {
		t.Fatalf("summarize: %d %s %s", code, out, errOut)
	}
	if code, _, _ := runMain("summarize", "--results", results, "--admission", admission, "--case", "H07"); code != SummaryPartial {
		t.Fatalf("selection exit %d", code)
	}
	other := filepath.Join(t.TempDir(), "other.json")
	adm, _ := os.ReadFile(admission)
	if err := os.WriteFile(other, append(adm, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runMain("summarize", "--results", results, "--admission", other); code != SummaryFailed {
		t.Fatalf("another manifest exit %d", code)
	}
	for _, args := range [][]string{
		{"summarize", "--results", "relative", "--admission", admission},
		{"summarize", "--results", results},
		{"record", "--observation", obsPath, "--results", results},
	} {
		if code, _, _ := runMain(args...); code != 2 {
			t.Errorf("%q exit %d, want 2", args, code)
		}
	}
}

func TestBuildRecord(t *testing.T) {
	m := testMatrix(t)
	run := testRun(t)
	exe := run.Manifest.Artifacts[0].SHA256
	good := Observation{Key: "H22/all", Kind: KindPrimary, Result: ResultPass, ExecutionID: "run-1", Sequence: 3, BootID: "boot-1"}
	r, err := BuildRecord(m, good, nil, run, exe)
	if err != nil || r.Source != testSource || r.Admission != run.Hash || r.Matrix != MatrixHash() || r.Executable != exe {
		t.Fatalf("record %+v %v", r, err)
	}
	control := Observation{Key: "H14/A#restore", Kind: KindControl, Result: ResultPass, Sequence: 4, BootID: "boot-1"}
	if _, err := BuildRecord(m, control, nil, run, exe); err != nil {
		t.Fatalf("control: %v", err)
	}
	for name, mutate := range map[string]func(*Observation){
		"reference key":  func(o *Observation) { o.Key = "H10/A" },
		"unknown key":    func(o *Observation) { o.Key = "H07/C" },
		"control kind":   func(o *Observation) { o.Kind = KindControl },
		"kind":           func(o *Observation) { o.Kind = "extra" },
		"result":         func(o *Observation) { o.Result = "maybe" },
		"no execution":   func(o *Observation) { o.ExecutionID = "" },
		"execution text": func(o *Observation) { o.ExecutionID = "a b" },
		"no sequence":    func(o *Observation) { o.Sequence = 0 },
		"no boot":        func(o *Observation) { o.BootID = "" },
		"multi-line":     func(o *Observation) { o.Detail = "a\nb" },
		"long detail":    func(o *Observation) { o.Detail = strings.Repeat("x", 513) },
	} {
		o := good
		mutate(&o)
		if _, err := BuildRecord(m, o, nil, run, exe); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := BuildRecord(m, good, nil, AdmittedRun{}, exe); err == nil {
		t.Error("record without an admitted run")
	}

	// An observed case's report: admitted, valid and from the record's boot.
	e := m.Expand()[slices.IndexFunc(m.Expand(), func(e Entry) bool { return e.Key == "H07/A" })]
	report := observerFor(e)
	observed := Observation{Key: "H07/A", Kind: KindPrimary, Result: ResultPass, ExecutionID: "run-2", Sequence: 5, BootID: report.Boot.String()}
	if r, err := BuildRecord(m, observed, report, run, exe); err != nil || r.Evidence.Observer != report {
		t.Fatalf("observed record %v", err)
	}
	failed := observed
	failed.Result = ResultFail
	if _, err := BuildRecord(m, failed, nil, run, exe); err != nil {
		t.Fatalf("failed observed record without a report: %v", err)
	}
	for name, mutate := range map[string]func(*Observation, *ObserverReport){
		"no report":          func(o *Observation, r *ObserverReport) { *r = ObserverReport{} },
		"another boot":       func(o *Observation, r *ObserverReport) { o.BootID = "boot-1" },
		"unadmitted":         func(o *Observation, r *ObserverReport) { r.Executable = strings.Repeat("5", 64) },
		"unfinished":         func(o *Observation, r *ObserverReport) { r.Stage = ObserverRunning },
		"unobserved case":    func(o *Observation, r *ObserverReport) { o.Key = "H22/all" },
		"inline report":      func(o *Observation, r *ObserverReport) { o.Evidence.Observer = r },
		"generation reorder": func(o *Observation, r *ObserverReport) { r.Generations[0].Seen = r.Generations[0].Created - 1 },
	} {
		o, r := observed, *report
		r.Generations = slices.Clone(report.Generations)
		mutate(&o, &r)
		if _, err := BuildRecord(m, o, &r, run, exe); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	var o Observation
	if err := decodeStrict([]byte(`{"key":"H07/A","kind":"primary","extra":1}`), &o); err == nil {
		t.Error("observation with an unknown field decoded")
	}
}

func TestEchoRoundTrip(t *testing.T) {
	s, err := ListenEcho("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := EchoRoundTrip("loopback", s.Addr(), testNonce, 5*time.Second)
	if !r.Connected || !r.Echoed || r.Sent != 33 || r.Error != 0 {
		t.Fatalf("round trip %+v", r)
	}
	if r := EchoRoundTrip("loopback", s.Addr(), "not-a-nonce", 5*time.Second); r.Echoed {
		t.Fatalf("garbage echoed %+v", r)
	}
	rep := s.Report()
	if len(rep.Received) != 1 || rep.Received[0] != testNonce || rep.Rejected != 1 {
		t.Fatalf("report %+v", rep)
	}
	addr := s.Addr()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if r := EchoRoundTrip("peer", addr, testNonce, time.Second); r.Connected || r.Error == 0 {
		t.Fatalf("closed listener %+v", r)
	}
	if code, _, _ := runMain("echo-serve", "--listen", "nowhere", "--report", "/tmp/x"); code != 2 {
		t.Fatal("echo-serve accepted a bad address")
	}
}

func TestPadLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	if err := os.WriteFile(path, []byte(`{"code":"daemon.open"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	size, err := PadLog(path, 262145, 300000)
	if err != nil || size != 262145 {
		t.Fatalf("padded %d %v", size, err)
	}
	data, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(data), `{"code":"daemon.open"}`+"\n") || strings.TrimRight(string(data[23:]), " \n") != "" || data[len(data)-1] != '\n' {
		t.Fatal("padding wrote something other than spaces and a newline")
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if size, err := PadLog(path, 262145, 300000); err != nil || size != 262145 {
		t.Fatalf("already padded: %d %v", size, err)
	}
	if _, err := PadLog(path, 262145, 1000); err == nil {
		t.Fatal("a log over the bound was padded")
	}
	for _, p := range []string{dir, filepath.Join(dir, "missing.log")} {
		if _, err := PadLog(p, 10, 100); err == nil {
			t.Fatalf("%s padded", p)
		}
	}
	if code, _, _ := runMain("pad-log", "--path", "relative.log"); code != 2 {
		t.Fatal("pad-log accepted a relative path")
	}
}

func TestProbeConfig(t *testing.T) {
	dir := t.TempDir()
	write := func(v any) string {
		t.Helper()
		data, _ := json.Marshal(v)
		path := filepath.Join(dir, "config.json")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	good := ProbeConfig{UnitFile: "/u/units/background.service", PeerRoot: "/peer", Absent: "/absent", Denied: "/denied",
		Loopback: "127.0.0.1:7401", Peer: "192.0.2.10:7401", Pipe: `\\.\pipe\winunitd-qual\h15`,
		SMB: &SMBTarget{Server: "peer", Path: `\\peer\share\nonce.txt`, ExpectSHA256: testContent},
		EFS: &EFSTarget{Path: "/b/secret.txt", Plain: "/b/plain.txt", ExpectSHA256: testContent}}
	if _, err := LoadProbeConfig(write(good)); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ProbeConfig){
		"relative path":     func(c *ProbeConfig) { c.Absent = "absent" },
		"public loopback":   func(c *ProbeConfig) { c.Loopback = "192.0.2.1:7401" },
		"peer without port": func(c *ProbeConfig) { c.Peer = "peer" },
		"pipe outside":      func(c *ProbeConfig) { c.Pipe = `\\.\pipe\winunitd\control` },
		"smb other server": func(c *ProbeConfig) {
			c.SMB = &SMBTarget{Server: "peer", Path: `\\other\share\x`, ExpectSHA256: testContent}
		},
		"smb no hash":  func(c *ProbeConfig) { c.SMB = &SMBTarget{Server: "peer", Path: `\\peer\share\x`} },
		"efs relative": func(c *ProbeConfig) { c.EFS = &EFSTarget{Path: "x", Plain: "/p", ExpectSHA256: testContent} },
	} {
		c := good
		mutate(&c)
		if _, err := LoadProbeConfig(write(c)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"peer":"x:1","password":"secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProbeConfig(filepath.Join(dir, "config.json")); err == nil {
		t.Fatal("a config with a credential field decoded")
	}
}

func TestDecodeAdmission(t *testing.T) {
	run := testRun(t)
	if run.Manifest.Source != testSource || len(run.Hash) != 64 {
		t.Fatalf("run %+v", run)
	}
	for name, data := range map[string]string{
		"dirty":         `{"schema":1,"source":"` + testSource + `","dirty":true,"artifacts":[{"name":"a","sha256":"` + testContent + `"}]}`,
		"short source":  `{"schema":1,"source":"0123456","dirty":false,"artifacts":[{"name":"a","sha256":"` + testContent + `"}]}`,
		"no artifacts":  `{"schema":1,"source":"` + testSource + `","dirty":false,"artifacts":[]}`,
		"trailing data": `{"schema":1,"source":"` + testSource + `","dirty":false,"artifacts":[{"name":"a","sha256":"` + testContent + `"}]}]`,
	} {
		if _, err := DecodeAdmission([]byte(data)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// File errors in command and test output name files, never the directories
// that can hold an account's profile or a private share.
func TestFileErrorsDoNotNameDirectories(t *testing.T) {
	private := filepath.Join(t.TempDir(), "S-1-5-21-7-7-7-1001", "profile")
	errs := map[string]error{
		"write record": WriteRecord(private, Record{Key: "H13/A"}),
		"write report": writeJSONFile(filepath.Join(private, "report.json"), 1),
	}
	_, errs["admission"] = LoadAdmission(filepath.Join(private, "admission.json"))
	_, errs["records"] = ReadRecords(private)
	_, errs["hash"] = FileSHA256(filepath.Join(private, "binary.exe"))
	_, errs["config"] = LoadProbeConfig(filepath.Join(private, "config.json"))
	for name, err := range errs {
		if err == nil || strings.Contains(err.Error(), "S-1-5-21-7-7-7-1001") {
			t.Errorf("%s: %v", name, err != nil)
		}
	}
	code, _, errOut := runMain("record", "--observation", filepath.Join(private, "observation.json"), "--results", private,
		"--admission", filepath.Join(private, "admission.json"))
	if code != 1 || strings.Contains(errOut, "S-1-5-21-7-7-7-1001") || !strings.Contains(errOut, "admission.json") {
		t.Fatalf("record diagnostic: %d, names the directory %t", code, strings.Contains(errOut, "S-1-5-21-7-7-7-1001"))
	}
}
