package nestedjob

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func absDir(t *testing.T) string {
	t.Helper()
	return filepath.Clean(t.TempDir())
}

func TestParseRoundTrip(t *testing.T) {
	dir := absDir(t)
	mains := []MainConfig{
		{LaunchMode: ModeAssign, CaseDir: dir, Generation: 1, Work: WorkIdle, OnStop: OnStopCooperative},
		{LaunchMode: ModeJobList, CaseDir: dir, Generation: 0, Work: WorkCommit, OnStop: OnStopIgnore},
		{LaunchMode: ModeAssign, CaseDir: dir, Generation: 7, Work: WorkCPU, OnStop: OnStopCooperative, Gate: GatePreAssign},
		{LaunchMode: ModeJobList, CaseDir: dir, Generation: 2, Work: WorkIdle, OnStop: OnStopCooperative, Gate: GatePreResume},
		{LaunchMode: ModeJobList, CaseDir: dir, Generation: 3, Work: WorkIdle, OnStop: OnStopCooperative, Sensitivity: SensitivityInheritInner},
	}
	for _, want := range mains {
		inv, err := Parse(want.Args())
		if err != nil {
			t.Fatalf("%v: %v", want.Args(), err)
		}
		if inv.Role != RoleMain || inv.Main == nil || *inv.Main != want {
			t.Fatalf("round trip %+v, want %+v", inv.Main, want)
		}
	}
	engine := EngineConfig{CaseDir: dir, Generation: 4, JobHandle: 0x1a4, Work: WorkCPU}
	if inv, err := Parse(engine.Args()); err != nil || inv.Engine == nil || *inv.Engine != engine {
		t.Fatalf("engine round trip %+v %v", inv.Engine, err)
	}
	for _, role := range []string{RoleG1, RoleG2, RoleProbe} {
		leaf := LeafConfig{CaseDir: dir, Generation: 4, Role: role, JobHandle: 8, Work: WorkCommit}
		if inv, err := Parse(leaf.Args()); err != nil || inv.Leaf == nil || *inv.Leaf != leaf {
			t.Fatalf("leaf round trip %+v %v", inv.Leaf, err)
		}
	}
	for _, b := range []string{StopCooperative, StopHang} {
		stop := StopConfig{CaseDir: dir, Behavior: b}
		if inv, err := Parse(stop.Args()); err != nil || inv.Stop == nil || *inv.Stop != stop {
			t.Fatalf("stop round trip %+v %v", inv.Stop, err)
		}
	}
}

func TestParseRejectsMalformedCommandLines(t *testing.T) {
	dir := absDir(t)
	main := func(extra ...string) []string {
		return append([]string{RoleMain, "--launch-mode", ModeAssign, "--case-dir", dir, "--generation", "1"}, extra...)
	}
	cases := map[string][]string{
		"no role":                 nil,
		"unknown role":            {"shell", "--case-dir", dir},
		"unknown flag":            main("--exec", "cmd.exe"),
		"positional":              main("extra"),
		"relative dir":            {RoleMain, "--launch-mode", ModeAssign, "--case-dir", "case", "--generation", "1"},
		"unclean dir":             {RoleMain, "--launch-mode", ModeAssign, "--case-dir", dir + string(filepath.Separator) + ".", "--generation", "1"},
		"missing mode":            {RoleMain, "--case-dir", dir, "--generation", "1"},
		"fallback mode":           {RoleMain, "--launch-mode", "auto", "--case-dir", dir, "--generation", "1"},
		"generation zero":         {RoleMain, "--launch-mode", ModeAssign, "--case-dir", dir, "--generation", "0"},
		"generation padded":       {RoleMain, "--launch-mode", ModeAssign, "--case-dir", dir, "--generation", "01"},
		"generation too large":    {RoleMain, "--launch-mode", ModeAssign, "--case-dir", dir, "--generation", "10000"},
		"missing generation":      {RoleMain, "--launch-mode", ModeAssign, "--case-dir", dir},
		"work":                    main("--engine-mode", "fork-bomb"),
		"on-stop":                 main("--on-stop", "exit"),
		"gate":                    main("--hold-gate", "after-resume"),
		"job-list pre-assign":     {RoleMain, "--launch-mode", ModeJobList, "--case-dir", dir, "--generation", "1", "--hold-gate", GatePreAssign},
		"gate with sensitivity":   main("--hold-gate", GatePreResume, "--sensitivity", SensitivityInheritInner),
		"sensitivity":             main("--sensitivity", "inherit-all"),
		"engine auto generation":  {RoleEngine, "--case-dir", dir, "--generation", "auto", "--job-handle-number", "4"},
		"engine without probe":    {RoleEngine, "--case-dir", dir, "--generation", "1"},
		"leaf main role":          {"leaf", "--case-dir", dir, "--generation", "1", "--role", RoleMain, "--job-handle-number", "4"},
		"stop behavior":           {RoleStop, "--case-dir", dir, "--behavior", "kill"},
		"stop without dir":        {RoleStop, "--behavior", StopHang},
		"matrix with case dir":    {"matrix", "--case-dir", dir},
		"matrix unknown lane":     {"matrix", "--lane", "ci"},
		"matrix unknown identity": {"matrix", "--identity", "admin"},
	}
	for name, args := range cases {
		if _, err := Parse(args); err == nil {
			t.Errorf("%s: %q accepted", name, args)
		}
	}
}

func TestHelperArgs(t *testing.T) {
	rest, ok := HelperArgs([]string{`C:\t.test.exe`, HelperSelector, RoleStop, "--behavior", StopHang})
	if !ok || !reflect.DeepEqual(rest, []string{RoleStop, "--behavior", StopHang}) {
		t.Fatalf("args = %q %v", rest, ok)
	}
	if _, ok := HelperArgs([]string{`C:\t.test.exe`, "-winunitd-helper=sleep"}); ok {
		t.Fatal("another helper selected the fixture")
	}
}

func testIdentity(role string, pid uint32, gen int) Identity {
	return Identity{Role: role, PID: pid, Created: 1000 + uint64(pid), Image: `C:\fixture.exe`,
		ImageSHA256: strings.Repeat("ab", 32), SID: "S-1-5-18", Generation: gen}
}

func testTree(gen int) []Member {
	return []Member{
		{Identity: testIdentity(RoleMain, 10, gen)},
		{Identity: testIdentity(RoleEngine, 11, gen), InInner: true},
		{Identity: testIdentity(RoleG1, 12, gen), InInner: true},
		{Identity: testIdentity(RoleG2, 13, gen), InInner: true},
	}
}

func writeEvents(t *testing.T, gen int, events ...Event) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	w := &reportWriter{w: &buf, generation: gen, now: func() time.Time { return time.Unix(1, 0) }}
	for _, e := range events {
		if err := w.emit(e); err != nil {
			t.Fatal(err)
		}
	}
	return &buf
}

func validEvents(gen int) []Event {
	main := testIdentity(RoleMain, 10, gen)
	engine := Identity{Role: RoleEngine, PID: 11, Created: 1011, ParentPID: 10, ParentCreated: 1010, Generation: gen}
	return []Event{
		{Kind: EventStart, Mode: ModeJobList, OS: "10.0.20348", Identity: &main},
		{Kind: EventPrevious, Note: "none"},
		{Kind: EventInnerJob, Inner: &InnerJob{Handle: 0x1a4, LimitFlags: 0x2000}},
		{Kind: EventCreated, Identity: &engine},
		{Kind: EventResumed, Identity: &engine},
		{Kind: EventTree, Tree: testTree(gen), Inner: &InnerJob{Handle: 0x1a4, LimitFlags: 0x2000}},
		{Kind: EventCommand, Note: "observed 1"},
		{Kind: EventReady},
	}
}

func TestReportRoundTripAndReadiness(t *testing.T) {
	buf := writeEvents(t, 3, validEvents(3)...)
	r, err := DecodeReport(buf, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Events) != 8 || r.Events[7].Seq != 8 || !r.Ready() || r.Fatal() != nil || r.Truncated {
		t.Fatalf("report = %+v", r)
	}
	if tree := r.Find(EventTree); tree == nil || !tree.Tree[1].InInner || tree.Tree[0].InInner {
		t.Fatal("tree membership did not round trip")
	}
	// READY without a preceding tree is not readiness.
	buf = writeEvents(t, 1, validEvents(1)[0], Event{Kind: EventReady})
	if r, err := DecodeReport(buf, 1); err != nil || r.Ready() {
		t.Fatalf("ready without tree: %v %v", r, err)
	}
	// MAIN may be appending: a trailing partial line is not yet an event.
	buf = writeEvents(t, 1, validEvents(1)[:2]...)
	buf.WriteString(`{"seq":3,"time":"`)
	if r, err := DecodeReport(buf, 1); err != nil || len(r.Events) != 2 {
		t.Fatalf("partial line: %v %v", r, err)
	}
	fatal := writeEvents(t, 1, validEvents(1)[0], Event{Kind: EventFatal, Failure: &Failure{Op: "assign-inner", Win32: 5, Message: "Access is denied."}})
	if r, err := DecodeReport(fatal, 1); err != nil || r.Fatal() == nil || r.Fatal().Failure.Win32 != 5 {
		t.Fatalf("fatal: %v %v", r, err)
	}
}

func TestDecodeReportRejectsMalformedEvents(t *testing.T) {
	line := func(e Event) string {
		e.Time = time.Unix(1, 0).UTC().Format(time.RFC3339Nano)
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		return string(b) + "\n"
	}
	main := testIdentity(RoleMain, 10, 1)
	start := line(Event{Seq: 1, Kind: EventStart, Generation: 1, Mode: ModeAssign, OS: "10.0", Identity: &main})
	badHash := main
	badHash.ImageSHA256 = "abc"
	threeTree := Event{Seq: 2, Kind: EventTree, Generation: 1, Tree: testTree(1)[:3], Inner: &InnerJob{}}
	swapped := testTree(1)
	swapped[2], swapped[3] = swapped[3], swapped[2]
	cases := map[string]string{
		"sequence gap":        start + line(Event{Seq: 3, Kind: EventReady, Generation: 1}),
		"repeated sequence":   start + line(Event{Seq: 1, Kind: EventReady, Generation: 1}),
		"generation mismatch": start + line(Event{Seq: 2, Kind: EventReady, Generation: 2}),
		"first not start":     line(Event{Seq: 1, Kind: EventReady, Generation: 1}),
		"unknown kind":        start + line(Event{Seq: 2, Kind: "exec", Generation: 1}),
		"unknown field":       strings.TrimSuffix(start, "}\n") + `,"command":"cmd.exe"}` + "\n",
		"bad image hash":      line(Event{Seq: 1, Kind: EventStart, Generation: 1, Mode: ModeAssign, OS: "10.0", Identity: &badHash}),
		"start without os":    line(Event{Seq: 1, Kind: EventStart, Generation: 1, Mode: ModeAssign, Identity: &main}),
		"three-member tree":   start + line(threeTree),
		"tree order":          start + line(Event{Seq: 2, Kind: EventTree, Generation: 1, Tree: swapped, Inner: &InnerJob{}}),
		"fatal without cause": start + line(Event{Seq: 2, Kind: EventFatal, Generation: 1}),
		"previous state":      start + line(Event{Seq: 2, Kind: EventPrevious, Generation: 1, Previous: []PreviousCheck{{Role: RoleMain, PID: 1, Created: 1, State: "assumed"}}}),
		"after truncation":    start + line(Event{Seq: 2, Kind: EventTruncated, Generation: 1}) + line(Event{Seq: 3, Kind: EventReady, Generation: 1}),
		"trailing data":       strings.TrimSuffix(start, "\n") + " {}\n",
		"oversized line":      start + `{"seq":2,"note":"` + strings.Repeat("x", MaxLineBytes) + `"}` + "\n",
		"oversized report":    strings.Repeat(" ", MaxReportBytes+1),
	}
	for name, data := range cases {
		if _, err := DecodeReport(strings.NewReader(data), 1); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestReportWriterTruncatesAtBound(t *testing.T) {
	var buf bytes.Buffer
	w := &reportWriter{w: &buf, generation: 1, now: time.Now}
	main := testIdentity(RoleMain, 10, 1)
	if err := w.emit(Event{Kind: EventStart, Mode: ModeAssign, OS: "10.0", Identity: &main}); err != nil {
		t.Fatal(err)
	}
	note := strings.Repeat("n", 32<<10)
	var err error
	for i := 0; err == nil && i < 1000; i++ {
		err = w.emit(Event{Kind: EventCommand, Note: note})
	}
	if err == nil || buf.Len() > MaxReportBytes {
		t.Fatalf("writer did not stop within the bound: %d bytes, %v", buf.Len(), err)
	}
	if w.emit(Event{Kind: EventReady}) == nil {
		t.Fatal("full report accepted another event")
	}
	r, err := DecodeReport(bytes.NewReader(buf.Bytes()), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Truncated || r.Events[len(r.Events)-1].Kind != EventTruncated {
		t.Fatal("bounded report lacks its truncation marker")
	}
	if over := (&reportWriter{w: &buf, generation: 1, now: time.Now}).emit(Event{Kind: EventCommand, Note: strings.Repeat("x", MaxLineBytes)}); over == nil {
		t.Fatal("oversized event accepted")
	}
}

func TestManifestGenerations(t *testing.T) {
	dir := absDir(t)
	if _, err := ReadManifest(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing manifest: %v", err)
	}
	if n, err := NextGeneration(nil, 0); err != nil || n != 1 {
		t.Fatalf("first generation %d %v", n, err)
	}
	m := Manifest{Generation: 3, Mode: ModeAssign, Stage: StageTree, Identities: []Identity{
		testIdentity(RoleMain, 10, 3), testIdentity(RoleEngine, 11, 3), testIdentity(RoleG1, 12, 3), testIdentity(RoleG2, 13, 3),
	}}
	if err := WriteJSON(filepath.Join(dir, ManifestFile), m); err != nil {
		t.Fatal(err)
	}
	got, err := ReadManifest(dir)
	if err != nil || !reflect.DeepEqual(*got, m) {
		t.Fatalf("manifest round trip %+v %v", got, err)
	}
	if n, err := NextGeneration(got, 0); err != nil || n != 4 {
		t.Fatalf("next generation %d %v", n, err)
	}
	// A failed attempt that claimed gen-0004 without publishing a manifest
	// keeps its evidence; the next attempt takes gen-0005.
	for _, name := range []string{GenerationDir(2), GenerationDir(4), "gen-04", "gen-00x5", "other"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, GenerationDir(9)), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	claimed, err := HighestClaimedGeneration(dir)
	if err != nil || claimed != 4 {
		t.Fatalf("highest claimed %d %v", claimed, err)
	}
	if n, err := NextGeneration(got, claimed); err != nil || n != 5 {
		t.Fatalf("generation after a failed claim %d %v", n, err)
	}
	if n, err := NextGeneration(nil, claimed); err != nil || n != 5 {
		t.Fatalf("generation without manifest %d %v", n, err)
	}
	if _, err := NextGeneration(&Manifest{Generation: MaxGeneration}, 0); err == nil {
		t.Fatal("generation space did not end")
	}
	bad := map[string]Manifest{
		"duplicate identity": {Generation: 1, Mode: ModeAssign, Stage: StageTree, Identities: []Identity{testIdentity(RoleMain, 10, 1), testIdentity(RoleMain, 10, 1)}},
		"other generation":   {Generation: 2, Mode: ModeAssign, Stage: StageTree, Identities: []Identity{testIdentity(RoleMain, 10, 1)}},
		"stage":              {Generation: 1, Mode: ModeAssign, Stage: "ready", Identities: []Identity{testIdentity(RoleMain, 10, 1)}},
		"main not first":     {Generation: 1, Mode: ModeAssign, Stage: StageGate, Identities: []Identity{testIdentity(RoleEngine, 11, 1)}},
		"no creation time":   {Generation: 1, Mode: ModeAssign, Stage: StageStart, Identities: []Identity{{Role: RoleMain, PID: 10, Generation: 1}}},
	}
	for name, m := range bad {
		if err := WriteJSON(filepath.Join(dir, ManifestFile), m); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadManifest(dir); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), bytes.Repeat([]byte(" "), MaxStatusBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(dir); err == nil {
		t.Error("oversized manifest accepted")
	}
}

func TestValidateCommandAllowsOnlyEnumeratedVerbs(t *testing.T) {
	ok := []struct {
		role string
		cmd  Command
	}{
		{RoleMain, Command{Seq: 1, Verb: VerbObserved}},
		{RoleMain, Command{Seq: 2, Verb: VerbSetInnerBreakaway, InnerBreakaway: InnerBreakawaySilent}},
		{RoleMain, Command{Seq: 3, Verb: VerbCheckInner, PID: 4, Created: 5}},
		{RoleMain, Command{Seq: 4, Verb: VerbProbe, Breakaway: true}},
		{RoleEngine, Command{Seq: 1, Verb: VerbCloseInherited}},
		{RoleG1, Command{Seq: 1, Verb: VerbStartWork}},
		{RoleG2, Command{Seq: 1, Verb: VerbProbe}},
	}
	for _, c := range ok {
		if err := ValidateCommand(c.role, c.cmd); err != nil {
			t.Errorf("%s %+v: %v", c.role, c.cmd, err)
		}
	}
	bad := []struct {
		role string
		cmd  Command
	}{
		{RoleMain, Command{Seq: 1, Verb: "exec"}},
		{RoleMain, Command{Seq: 0, Verb: VerbPing}},
		{RoleMain, Command{Seq: 1, Verb: VerbStartWork}},
		{RoleMain, Command{Seq: 1, Verb: VerbSetInnerBreakaway, InnerBreakaway: "both"}},
		{RoleMain, Command{Seq: 1, Verb: VerbCheckInner, PID: 4}},
		{RoleMain, Command{Seq: 1, Verb: VerbPing, PID: 4}},
		{RoleEngine, Command{Seq: 1, Verb: VerbCloseInner}},
		{RoleEngine, Command{Seq: 1, Verb: VerbStartWork, Breakaway: true}},
		{RoleG1, Command{Seq: 1, Verb: VerbCloseInherited}},
		{RoleProbe, Command{Seq: 1, Verb: VerbPing}},
		{RoleStop, Command{Seq: 1, Verb: VerbPing}},
	}
	for _, c := range bad {
		if err := ValidateCommand(c.role, c.cmd); err == nil {
			t.Errorf("%s %+v accepted", c.role, c.cmd)
		}
	}
}

// readyEvents is a consistent READY report for mode.
func readyEvents(mode string) []Event {
	main := testIdentity(RoleMain, 10, 1)
	engine := testIdentity(RoleEngine, 11, 1)
	engine.ParentPID, engine.ParentCreated = main.PID, main.Created
	tree := []Member{{Identity: main}, {Identity: engine, InInner: true}}
	for i, role := range []string{RoleG1, RoleG2} {
		leaf := testIdentity(role, uint32(12+i), 1)
		leaf.ParentPID, leaf.ParentCreated = engine.PID, engine.Created
		tree = append(tree, Member{Identity: leaf, InInner: true})
	}
	created := Identity{Role: RoleEngine, PID: engine.PID, Created: engine.Created, ParentPID: main.PID, ParentCreated: main.Created, Generation: 1}
	inner := &InnerJob{Handle: 0x1a4, LimitFlags: jobLimitKillOnJobClose}
	events := []Event{
		{Kind: EventStart, Mode: mode, OS: "10.0.20348", Identity: &main},
		{Kind: EventPrevious, Note: "none"},
		{Kind: EventInnerJob, Inner: inner},
		{Kind: EventCreated, Identity: &created},
	}
	if mode == ModeAssign {
		events = append(events, Event{Kind: EventAssigned, Identity: &created})
	}
	return append(events,
		Event{Kind: EventResumed, Identity: &created},
		Event{Kind: EventTree, Tree: tree, Inner: inner},
		Event{Kind: EventCommand, Note: "observed 1"},
		Event{Kind: EventReady},
	)
}

func TestCheckTree(t *testing.T) {
	decode := func(events []Event) *Report {
		t.Helper()
		r, err := DecodeReport(writeEvents(t, 1, events...), 1)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	for _, mode := range LaunchModes {
		if err := CheckTree(decode(readyEvents(mode)), mode); err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
	}
	treeIndex := func(events []Event) int {
		for i, e := range events {
			if e.Kind == EventTree {
				return i
			}
		}
		return -1
	}
	mutations := map[string]func([]Event) []Event{
		"main in inner": func(ev []Event) []Event { ev[treeIndex(ev)].Tree[0].InInner = true; return ev },
		"leaf outside":  func(ev []Event) []Event { ev[treeIndex(ev)].Tree[3].InInner = false; return ev },
		"leaf parent":   func(ev []Event) []Event { ev[treeIndex(ev)].Tree[2].ParentPID = 10; return ev },
		"engine parent": func(ev []Event) []Event { ev[treeIndex(ev)].Tree[1].ParentCreated = 1; return ev },
		"token":         func(ev []Event) []Event { ev[treeIndex(ev)].Tree[2].SID = "S-1-5-21-1"; return ev },
		"elevation":     func(ev []Event) []Event { ev[treeIndex(ev)].Tree[3].Elevated = true; return ev },
		"image":         func(ev []Event) []Event { ev[treeIndex(ev)].Tree[1].ImageSHA256 = strings.Repeat("cd", 32); return ev },
		"breakaway": func(ev []Event) []Event {
			ev[treeIndex(ev)].Inner = &InnerJob{LimitFlags: jobLimitKillOnJobClose | jobLimitBreakawayOK}
			return ev
		},
		"inheritable": func(ev []Event) []Event {
			ev[treeIndex(ev)].Inner = &InnerJob{LimitFlags: jobLimitKillOnJobClose, Inheritable: true}
			return ev
		},
		"inner cpu": func(ev []Event) []Event {
			ev[treeIndex(ev)].Inner = &InnerJob{LimitFlags: jobLimitKillOnJobClose, CPUControlFlags: 5}
			return ev
		},
		"no assignment": func(ev []Event) []Event { return append(ev[:4:4], ev[5:]...) },
		"gate": func(ev []Event) []Event {
			gate := Event{Kind: EventGate, Gate: GatePreResume, Tree: []Member{ev[treeIndex(ev)].Tree[1]}}
			return append(ev[:5:5], append([]Event{gate}, ev[5:]...)...)
		},
	}
	for name, mutate := range mutations {
		if err := CheckTree(decode(mutate(readyEvents(ModeAssign))), ModeAssign); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := CheckTree(decode(readyEvents(ModeJobList)), ModeAssign); err == nil {
		t.Error("launch mode mismatch accepted")
	}
	notReady := readyEvents(ModeJobList)
	if err := CheckTree(decode(notReady[:len(notReady)-1]), ModeJobList); err == nil {
		t.Error("tree without READY accepted")
	}
}

func TestCheckHandleProbes(t *testing.T) {
	statuses := map[string]RoleStatus{}
	for _, role := range []string{RoleEngine, RoleG1, RoleG2} {
		statuses[role] = RoleStatus{HandleProbe: HandleProbe{Value: 0x1a4, Win32: 6}}
	}
	if err := CheckHandleProbes(statuses, 0x1a4, ""); err != nil {
		t.Fatal(err)
	}
	if err := CheckHandleProbes(statuses, 0x1a8, ""); err == nil {
		t.Error("probe of another value accepted")
	}
	for _, code := range []uint32{0, 5, 87} {
		denied := statuses[RoleG1]
		denied.HandleProbe.Win32 = code
		statuses[RoleG1] = denied
		if err := CheckHandleProbes(statuses, 0x1a4, ""); err == nil {
			t.Errorf("probe error win32 %d accepted as no inherited job", code)
		}
	}
	restored := statuses[RoleG1]
	restored.HandleProbe.Win32 = 6
	statuses[RoleG1] = restored
	leaked := statuses[RoleG2]
	leaked.HandleProbe.IsJob = true
	statuses[RoleG2] = leaked
	if err := CheckHandleProbes(statuses, 0x1a4, ""); err == nil {
		t.Error("inherited grandchild job handle accepted")
	}
	if err := CheckHandleProbes(statuses, 0x1a4, RoleEngine); err == nil {
		t.Error("sensitivity control accepted a probe that missed ENGINE's handle")
	}
	delete(statuses, RoleG1)
	if err := CheckHandleProbes(statuses, 0x1a4, ""); err == nil {
		t.Error("missing status accepted")
	}
}
