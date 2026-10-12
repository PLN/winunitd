package nestedjob

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Bounds for every file the fixture reads or writes.
const (
	MaxReportBytes = 4 << 20
	MaxLineBytes   = 64 << 10
	MaxStatusBytes = 256 << 10
)

// Commit work: each descendant commits and touches 4 MiB steps up to 96 MiB.
const (
	CommitStep       = 4 << 20
	CommitPerProcess = 96 << 20
)

// Case directory names.
const (
	ManifestFile    = "generation.json"
	ReportFile      = "report.jsonl"
	StopRequestFile = "stop-request.json"
)

// GenerationDir is the per-generation directory name inside a case directory.
func GenerationDir(n int) string { return fmt.Sprintf("gen-%04d", n) }

// StatusFile names a role's self-written status file.
func StatusFile(role string) string { return "status-" + role + ".json" }

// WorkFile names a role's work-result file.
func WorkFile(role string) string { return "work-" + role + ".json" }

// CommandFile and AckFile name one enumerated command and its acknowledgment.
func CommandFile(role string, seq int) string { return fmt.Sprintf("cmd-%s-%04d.json", role, seq) }

// AckFile names the acknowledgment for CommandFile(role, seq).
func AckFile(role string, seq int) string { return fmt.Sprintf("ack-%s-%04d.json", role, seq) }

// StopHelperFile names one ExecStop helper invocation's record.
func StopHelperFile(pid uint32) string { return fmt.Sprintf("stop-helper-%d.json", pid) }

// Identity is one process as observed by itself or by its creator. Created is
// the creation FILETIME in 100 ns units; PID alone never identifies a process.
type Identity struct {
	Role          string `json:"role"`
	PID           uint32 `json:"pid"`
	Created       uint64 `json:"created"`
	ParentPID     uint32 `json:"parentPid,omitempty"`
	ParentCreated uint64 `json:"parentCreated,omitempty"`
	Image         string `json:"image,omitempty"`
	ImageSHA256   string `json:"imageSha256,omitempty"`
	SID           string `json:"sid,omitempty"`
	Session       uint32 `json:"session"`
	Elevated      bool   `json:"elevated"`
	Invocation    string `json:"invocation,omitempty"`
	Generation    int    `json:"generation"`
}

// Same reports whether both records name the same process instance.
func (id Identity) Same(other Identity) bool {
	return id.PID == other.PID && id.Created == other.Created
}

func (id Identity) validate(self bool) error {
	switch id.Role {
	case RoleMain, RoleEngine, RoleG1, RoleG2, RoleProbe, RoleStop, RoleOwner, RoleManagerOwner:
	default:
		return fmt.Errorf("identity role %q", id.Role)
	}
	if id.PID == 0 || id.Created == 0 {
		return fmt.Errorf("%s identity lacks pid or creation time", id.Role)
	}
	if id.Generation < 1 || id.Generation > MaxGeneration {
		return fmt.Errorf("%s identity generation %d", id.Role, id.Generation)
	}
	if !self {
		return nil
	}
	if id.Image == "" || id.SID == "" {
		return fmt.Errorf("%s self identity lacks image or token SID", id.Role)
	}
	if b, err := hex.DecodeString(id.ImageSHA256); err != nil || len(b) != 32 {
		return fmt.Errorf("%s image hash %q", id.Role, id.ImageSHA256)
	}
	return nil
}

// Member is a tree identity with MAIN's inner-job membership check and the
// process's committed private bytes at that moment. A failed measurement
// is reported as such, never as zero bytes.
type Member struct {
	Identity
	InInner           bool     `json:"inInner"`
	PrivateBytes      uint64   `json:"privateBytes,omitempty"`
	PrivateBytesError *Failure `json:"privateBytesError,omitempty"`
}

// InnerJob is MAIN's query of its own inner job and handle.
type InnerJob struct {
	Handle          uint64 `json:"handle"`
	LimitFlags      uint32 `json:"limitFlags"`
	CPUControlFlags uint32 `json:"cpuControlFlags"`
	CPUQueryError   string `json:"cpuQueryError,omitempty"`
	Inheritable     bool   `json:"inheritable"`
}

// Failure is a native or protocol error with its Win32 code when known.
type Failure struct {
	Op      string `json:"op"`
	Win32   uint32 `json:"win32,omitempty"`
	Message string `json:"message"`
}

func (f *Failure) Error() string {
	if f == nil {
		return ""
	}
	if f.Win32 != 0 {
		return fmt.Sprintf("%s: %s (win32 %d)", f.Op, f.Message, f.Win32)
	}
	return f.Op + ": " + f.Message
}

// Previous generation states. Only gone, exited and reused prove absence.
const (
	PreviousGone    = "gone"
	PreviousExited  = "exited"
	PreviousReused  = "reused"
	PreviousRunning = "running"
	PreviousUnknown = "unknown"
)

// PreviousCheck is MAIN's entry check of one previous-generation identity.
type PreviousCheck struct {
	Role    string `json:"role"`
	PID     uint32 `json:"pid"`
	Created uint64 `json:"created"`
	State   string `json:"state"`
	Error   string `json:"error,omitempty"`
}

// Report event kinds. Each has a fixed set of required fields (see validate).
const (
	EventStart         = "start"
	EventPrevious      = "previous"
	EventInnerJob      = "inner-job"
	EventCreated       = "created"
	EventAssigned      = "assigned"
	EventGate          = "gate"
	EventResumed       = "resumed"
	EventTree          = "tree"
	EventReady         = "ready"
	EventCommand       = "command"
	EventStopRequested = "stop-requested"
	EventStopIgnored   = "stop-ignored"
	EventInnerClosed   = "inner-closed"
	EventStopComplete  = "stop-complete"
	EventUnqualified   = "unqualified"
	EventFatal         = "fatal"
	EventTruncated     = "truncated"
)

// Event is one report line.
type Event struct {
	Seq        int             `json:"seq"`
	Time       string          `json:"time"`
	Kind       string          `json:"event"`
	Generation int             `json:"generation"`
	Mode       string          `json:"mode,omitempty"`
	OS         string          `json:"os,omitempty"`
	Gate       string          `json:"gate,omitempty"`
	Note       string          `json:"note,omitempty"`
	Identity   *Identity       `json:"identity,omitempty"`
	Tree       []Member        `json:"tree,omitempty"`
	Inner      *InnerJob       `json:"inner,omitempty"`
	Previous   []PreviousCheck `json:"previous,omitempty"`
	Failure    *Failure        `json:"failure,omitempty"`
}

func (e Event) validate() error {
	if _, err := time.Parse(time.RFC3339Nano, e.Time); err != nil {
		return fmt.Errorf("event %d time: %w", e.Seq, err)
	}
	need := func(ok bool, field string) error {
		if !ok {
			return fmt.Errorf("event %d (%s) lacks %s", e.Seq, e.Kind, field)
		}
		return nil
	}
	switch e.Kind {
	case EventStart:
		if err := need(e.Identity != nil && e.Mode != "" && e.OS != "", "identity, mode and os"); err != nil {
			return err
		}
		return e.Identity.validate(true)
	case EventPrevious:
		for _, p := range e.Previous {
			switch p.State {
			case PreviousGone, PreviousExited, PreviousReused, PreviousRunning, PreviousUnknown:
			default:
				return fmt.Errorf("event %d previous state %q", e.Seq, p.State)
			}
		}
		return nil
	case EventInnerJob:
		return need(e.Inner != nil, "inner")
	case EventCreated, EventAssigned, EventResumed:
		if err := need(e.Identity != nil, "identity"); err != nil {
			return err
		}
		return e.Identity.validate(false)
	case EventGate:
		if err := need(e.Gate != "" && len(e.Tree) == 1, "gate and held child"); err != nil {
			return err
		}
		return e.Tree[0].validate(false)
	case EventTree:
		if err := need(len(e.Tree) == 4 && e.Inner != nil, "four tree members and inner"); err != nil {
			return err
		}
		for i, m := range e.Tree {
			if m.Role != []string{RoleMain, RoleEngine, RoleG1, RoleG2}[i] {
				return fmt.Errorf("event %d tree order", e.Seq)
			}
			if err := m.validate(true); err != nil {
				return err
			}
		}
		return nil
	case EventCommand, EventStopRequested, EventStopIgnored, EventInnerClosed, EventStopComplete:
		return need(e.Note != "", "note")
	case EventUnqualified, EventFatal:
		return need(e.Failure != nil, "failure")
	case EventReady, EventTruncated:
		return nil
	default:
		return fmt.Errorf("event %d kind %q", e.Seq, e.Kind)
	}
}

// Report is a decoded, validated MAIN report. Partial means a trailing
// incomplete line was ignored, which is normal only while MAIN still writes.
type Report struct {
	Events    []Event
	Truncated bool
	Partial   bool
}

// Find returns the first event of kind, or nil.
func (r *Report) Find(kind string) *Event {
	for i := range r.Events {
		if r.Events[i].Kind == kind {
			return &r.Events[i]
		}
	}
	return nil
}

// Ready reports whether READY followed a complete tree.
func (r *Report) Ready() bool {
	tree := false
	for _, e := range r.Events {
		switch e.Kind {
		case EventTree:
			tree = true
		case EventReady:
			return tree
		}
	}
	return false
}

// Fatal returns the first fatal or unqualified event, or nil.
func (r *Report) Fatal() *Event {
	for i := range r.Events {
		if k := r.Events[i].Kind; k == EventFatal || k == EventUnqualified {
			return &r.Events[i]
		}
	}
	return nil
}

// DecodeReport strictly decodes a report. A trailing partial line is ignored
// because MAIN may be writing it; a complete malformed line is an error.
func DecodeReport(r io.Reader, generation int) (*Report, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxReportBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxReportBytes {
		return nil, fmt.Errorf("report exceeds %d bytes", MaxReportBytes)
	}
	out := &Report{}
	if i := bytes.LastIndexByte(data, '\n'); i >= 0 {
		out.Partial = i+1 < len(data)
		data = data[:i+1]
	} else {
		out.Partial = len(data) > 0
		data = nil
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 4096), MaxLineBytes)
	for sc.Scan() {
		if out.Truncated {
			return nil, errors.New("event after truncation marker")
		}
		var e Event
		if err := decodeStrict(sc.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("report line %d: %w", len(out.Events)+1, err)
		}
		if e.Seq != len(out.Events)+1 {
			return nil, fmt.Errorf("report sequence %d, want %d", e.Seq, len(out.Events)+1)
		}
		if e.Generation != generation {
			return nil, fmt.Errorf("report event %d generation %d, want %d", e.Seq, e.Generation, generation)
		}
		if e.Seq == 1 && e.Kind != EventStart {
			return nil, errors.New("report does not begin with start")
		}
		if err := e.validate(); err != nil {
			return nil, err
		}
		out.Truncated = e.Kind == EventTruncated
		out.Events = append(out.Events, e)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ReadFinalReport decodes a report whose writer has exited. A truncated
// report or an incomplete last line is not acceptance evidence.
func ReadFinalReport(caseDir string, generation int) (*Report, error) {
	r, err := ReadReport(caseDir, generation)
	if err != nil {
		return nil, err
	}
	switch {
	case r.Truncated:
		return r, errors.New("report was truncated")
	case r.Partial:
		return r, errors.New("report ends with an incomplete line")
	}
	return r, nil
}

// ReadReport decodes gen-NNNN/report.jsonl in caseDir.
func ReadReport(caseDir string, generation int) (*Report, error) {
	f, err := os.Open(filepath.Join(caseDir, GenerationDir(generation), ReportFile))
	if err != nil {
		return nil, baseOnly(err)
	}
	defer f.Close()
	return DecodeReport(f, generation)
}

// reportWriter appends bounded events. Only MAIN writes its report.
type reportWriter struct {
	w          io.Writer
	generation int
	seq        int
	size       int
	full       bool
	now        func() time.Time
}

// truncationReserve keeps room for the final truncation marker.
const truncationReserve = 256

func (w *reportWriter) emit(e Event) error {
	if w.full {
		return errors.New("report is full")
	}
	e.Seq = w.seq + 1
	e.Generation = w.generation
	e.Time = w.now().UTC().Format(time.RFC3339Nano)
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if len(line) > MaxLineBytes || w.size+len(line) > MaxReportBytes-truncationReserve {
		w.full = true
		marker, _ := json.Marshal(Event{Seq: e.Seq, Time: e.Time, Kind: EventTruncated, Generation: w.generation})
		marker = append(marker, '\n')
		if _, err := w.w.Write(marker); err != nil {
			return err
		}
		w.seq++
		w.size += len(marker)
		return errors.New("report is full")
	}
	if _, err := w.w.Write(line); err != nil {
		return err
	}
	w.seq++
	w.size += len(line)
	return nil
}

// Manifest is the case directory's current generation record. MAIN replaces it
// atomically whenever the set of identities it is responsible for grows.
type Manifest struct {
	Generation int        `json:"generation"`
	Invocation string     `json:"invocation,omitempty"`
	Mode       string     `json:"mode"`
	Stage      string     `json:"stage"`
	Identities []Identity `json:"identities"`
}

// Manifest stages.
const (
	StageStart = "start"
	StageGate  = "gate"
	StageTree  = "tree"
)

func (m *Manifest) validate() error {
	if m.Generation < 1 || m.Generation > MaxGeneration {
		return fmt.Errorf("manifest generation %d", m.Generation)
	}
	switch m.Stage {
	case StageStart, StageGate, StageTree:
	default:
		return fmt.Errorf("manifest stage %q", m.Stage)
	}
	if len(m.Identities) == 0 || m.Identities[0].Role != RoleMain {
		return errors.New("manifest does not begin with MAIN")
	}
	seen := map[string]bool{}
	for _, id := range m.Identities {
		if err := id.validate(false); err != nil {
			return err
		}
		if id.Generation != m.Generation {
			return fmt.Errorf("manifest identity generation %d, want %d", id.Generation, m.Generation)
		}
		key := strconv.FormatUint(uint64(id.PID), 10) + "/" + strconv.FormatUint(id.Created, 10)
		if seen[key] {
			return fmt.Errorf("manifest repeats pid %d", id.PID)
		}
		seen[key] = true
	}
	return nil
}

// ReadManifest reads and validates caseDir/generation.json. A missing
// manifest returns an error satisfying errors.Is(err, os.ErrNotExist).
func ReadManifest(caseDir string) (*Manifest, error) {
	var m Manifest
	if err := ReadJSON(filepath.Join(caseDir, ManifestFile), &m); err != nil {
		return nil, err
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// NextGeneration selects the generation after both the manifest (nil when
// none exists) and the highest claimed generation directory. A MAIN that
// failed before publishing its manifest keeps its claimed directory and
// report; the next attempt neither reuses nor overwrites that evidence.
func NextGeneration(prev *Manifest, claimed int) (int, error) {
	last := claimed
	if prev != nil {
		last = max(last, prev.Generation)
	}
	if last >= MaxGeneration {
		return 0, errors.New("generation space exhausted")
	}
	return last + 1, nil
}

// HighestClaimedGeneration returns the largest gen-NNNN directory in caseDir.
func HighestClaimedGeneration(caseDir string) (int, error) {
	entries, err := os.ReadDir(caseDir)
	if err != nil {
		return 0, err
	}
	highest := 0
	for _, e := range entries {
		var n int
		if !e.IsDir() || len(e.Name()) != len("gen-0000") {
			continue
		}
		if _, err := fmt.Sscanf(e.Name(), "gen-%04d", &n); err == nil && GenerationDir(n) == e.Name() {
			highest = max(highest, n)
		}
	}
	return highest, nil
}

// Command verbs. Each role accepts only its listed verbs.
const (
	VerbObserved          = "observed"
	VerbPing              = "ping"
	VerbCloseInner        = "close-inner"
	VerbSetInnerBreakaway = "set-inner-breakaway"
	VerbCheckInner        = "check-inner"
	VerbProbe             = "probe"
	VerbStartWork         = "start-work"
	VerbCloseInherited    = "close-inherited"
	// Owner-agent verbs.
	VerbLaunch    = "launch"
	VerbInUnitJob = "in-unit-job"
	VerbPIDs      = "pids"
	VerbStop      = "stop"
	VerbExit      = "exit"
	// Manager-owner verbs.
	VerbStart         = "start"
	VerbStopUnit      = "stop-unit"
	VerbInspect       = "inspect"
	VerbExpectDrained = "expect-drained"
)

// MaxHeldIdentities bounds an expect-drained request.
const MaxHeldIdentities = 16

// HelperState is one ExecStop helper the owner's launcher held from its
// creation, and whether it had exited when the view was taken.
type HelperState struct {
	PID     uint32   `json:"pid"`
	Created uint64   `json:"created"`
	Exited  bool     `json:"exited"`
	Error   *Failure `json:"error,omitempty"`
}

// ManagerView is a manager owner's bounded answer about its nested unit:
// lifecycle status, the unit job's native limits, the launches its
// observing launcher saw and the ExecStop helpers it holds. A failed
// observation is reported in InspectError, never as empty or zero state;
// HasJob false with no error means the unit deliberately has no job.
type ManagerView struct {
	InspectError         *Failure      `json:"inspectError,omitempty"`
	Helpers              []HelperState `json:"helpers,omitempty"`
	ActiveState          string        `json:"activeState"`
	Reason               string        `json:"reason,omitempty"`
	Error                string        `json:"error,omitempty"`
	InvocationID         string        `json:"invocationId,omitempty"`
	TerminationUncertain bool          `json:"terminationUncertain"`
	MainPID              int           `json:"mainPid,omitempty"`
	CPUQuota             uint32        `json:"cpuQuota,omitempty"`
	WindowsCPUQuota      uint32        `json:"windowsCPUQuota,omitempty"`
	StopHelpers          int           `json:"stopHelpers"`
	Launches             int           `json:"launches"`
	Violations           []string      `json:"violations,omitempty"`
	HasJob               bool          `json:"hasJob"`
	JobMemory            uint64        `json:"jobMemory,omitempty"`
	PeakJobMemory        uint64        `json:"peakJobMemory,omitempty"`
	LimitFlags           uint32        `json:"limitFlags,omitempty"`
	CPURate              uint32        `json:"cpuRate,omitempty"`
	CPUControlFlags      uint32        `json:"cpuControlFlags,omitempty"`
}

// Validate requires a view that observed a unit state and every native
// value it reports. Tests assert nothing on a view that fails it.
func (v ManagerView) Validate() error {
	if v.InspectError != nil {
		return fmt.Errorf("inspection failed: %s", SafeFailure(v.InspectError))
	}
	if v.ActiveState == "" {
		return errors.New("no observed unit state")
	}
	if !v.HasJob && (v.JobMemory != 0 || v.PeakJobMemory != 0 || v.LimitFlags != 0 || v.CPURate != 0 || v.CPUControlFlags != 0) {
		return errors.New("job limits without a job")
	}
	for _, h := range v.Helpers {
		if h.Error != nil || h.PID == 0 || h.Created == 0 {
			return fmt.Errorf("stop helper %d unobserved: %s", h.PID, SafeFailure(h.Error))
		}
	}
	return nil
}

// MaxOwnerStopMS bounds an owner agent's unit stop.
const MaxOwnerStopMS = 60000

// Inner breakaway settings for set-inner-breakaway.
const (
	InnerBreakawayExplicit = "explicit"
	InnerBreakawaySilent   = "silent"
)

// Command is one observer request.
type Command struct {
	Seq            int        `json:"seq"`
	Verb           string     `json:"verb"`
	Breakaway      bool       `json:"breakaway,omitempty"`
	InnerBreakaway string     `json:"innerBreakaway,omitempty"`
	PID            uint32     `json:"pid,omitempty"`
	Created        uint64     `json:"created,omitempty"`
	Args           []string   `json:"args,omitempty"`
	TimeoutMS      int64      `json:"timeoutMs,omitempty"`
	Held           []Identity `json:"held,omitempty"`
}

// ValidateCommand checks that role accepts cmd with well-formed arguments.
func ValidateCommand(role string, cmd Command) error {
	allowed := map[string][]string{
		RoleMain:         {VerbObserved, VerbPing, VerbCloseInner, VerbSetInnerBreakaway, VerbCheckInner, VerbProbe},
		RoleEngine:       {VerbPing, VerbProbe, VerbStartWork, VerbCloseInherited},
		RoleG1:           {VerbPing, VerbProbe, VerbStartWork},
		RoleG2:           {VerbPing, VerbProbe, VerbStartWork},
		RoleOwner:        {VerbPing, VerbLaunch, VerbInUnitJob, VerbPIDs, VerbStop, VerbExit},
		RoleManagerOwner: {VerbPing, VerbStart, VerbStopUnit, VerbInspect, VerbInUnitJob, VerbExpectDrained, VerbExit},
	}[role]
	ok := false
	for _, v := range allowed {
		ok = ok || v == cmd.Verb
	}
	if !ok {
		return fmt.Errorf("role %q does not accept %q", role, cmd.Verb)
	}
	if cmd.Seq < 1 || cmd.Seq > 9999 {
		return fmt.Errorf("command sequence %d", cmd.Seq)
	}
	switch cmd.Verb {
	case VerbSetInnerBreakaway:
		if cmd.InnerBreakaway != InnerBreakawayExplicit && cmd.InnerBreakaway != InnerBreakawaySilent {
			return fmt.Errorf("inner breakaway %q", cmd.InnerBreakaway)
		}
	case VerbCheckInner, VerbInUnitJob:
		if cmd.PID == 0 || cmd.Created == 0 {
			return fmt.Errorf("%s needs pid and creation time", cmd.Verb)
		}
	case VerbLaunch:
		// An agent launches only the fixture's main role, never other text.
		if inv, err := Parse(cmd.Args); err != nil || inv.Main == nil {
			return fmt.Errorf("launch needs main-role fixture arguments: %v", err)
		}
	case VerbStop:
		if cmd.TimeoutMS < 1 || cmd.TimeoutMS > MaxOwnerStopMS {
			return fmt.Errorf("stop timeout %d ms", cmd.TimeoutMS)
		}
	case VerbExpectDrained:
		if len(cmd.Held) == 0 || len(cmd.Held) > MaxHeldIdentities {
			return fmt.Errorf("expect-drained needs 1 to %d identities", MaxHeldIdentities)
		}
		for _, id := range cmd.Held {
			if id.PID == 0 || id.Created == 0 {
				return errors.New("expect-drained identities need pid and creation time")
			}
		}
	}
	if cmd.Verb != VerbSetInnerBreakaway && cmd.InnerBreakaway != "" ||
		cmd.Verb != VerbCheckInner && cmd.Verb != VerbInUnitJob && (cmd.PID != 0 || cmd.Created != 0) ||
		cmd.Verb != VerbProbe && cmd.Breakaway ||
		cmd.Verb != VerbLaunch && len(cmd.Args) > 0 ||
		cmd.Verb != VerbStop && cmd.TimeoutMS != 0 ||
		cmd.Verb != VerbExpectDrained && len(cmd.Held) > 0 {
		return fmt.Errorf("%s has arguments of another verb", cmd.Verb)
	}
	return nil
}

// ProbeResult reports one extra CreateProcess attempt. A created probe stays
// suspended and is never resumed; the observer verifies and tears it down.
type ProbeResult struct {
	Creator   string    `json:"creator"`
	Breakaway bool      `json:"breakaway"`
	Created   bool      `json:"created"`
	Failure   *Failure  `json:"failure,omitempty"`
	Identity  *Identity `json:"identity,omitempty"`
	InAnyJob  bool      `json:"inAnyJob"`
}

// Ack is a role's response to one command.
type Ack struct {
	Seq        int          `json:"seq"`
	Verb       string       `json:"verb"`
	OK         bool         `json:"ok"`
	Failure    *Failure     `json:"failure,omitempty"`
	Probe      *ProbeResult `json:"probe,omitempty"`
	InInner    *bool        `json:"inInner,omitempty"`
	LimitFlags uint32       `json:"limitFlags,omitempty"`
	Closed     int          `json:"closed,omitempty"`
	// Owner-agent results.
	Identity  *Identity    `json:"identity,omitempty"`
	InUnitJob *bool        `json:"inUnitJob,omitempty"`
	PIDs      []int        `json:"pids,omitempty"`
	Manager   *ManagerView `json:"manager,omitempty"`
	ElapsedMS int64        `json:"elapsedMs,omitempty"`
}

// HandleProbe is the negative inheritance probe at a numeric handle value.
type HandleProbe struct {
	Value   uint64 `json:"value"`
	IsJob   bool   `json:"isJob"`
	Win32   uint32 `json:"win32,omitempty"`
	Message string `json:"message,omitempty"`
}

// RoleStatus is written by ENGINE, G1 and G2 after they start running.
type RoleStatus struct {
	Identity          Identity    `json:"identity"`
	Children          []Identity  `json:"children,omitempty"`
	HandleProbe       HandleProbe `json:"handleProbe"`
	PrivateBytes      uint64      `json:"privateBytes"`
	PrivateBytesError *Failure    `json:"privateBytesError,omitempty"`
}

// WorkStatus records commit or CPU work progress and its native error.
type WorkStatus struct {
	Role           string   `json:"role"`
	Work           string   `json:"work"`
	CommittedBytes uint64   `json:"committedBytes"`
	Failure        *Failure `json:"failure,omitempty"`
}

// StopRequest is the ExecStop helper's cooperative request to MAIN.
type StopRequest struct {
	Helper  Identity `json:"helper"`
	MainPID uint32   `json:"mainPid"`
}

// StopHelperStatus is one helper invocation's record.
type StopHelperStatus struct {
	Identity    Identity `json:"identity"`
	Behavior    string   `json:"behavior"`
	MainPID     uint32   `json:"mainPid"`
	MainMatched bool     `json:"mainMatched"`
	MainExited  bool     `json:"mainExited"`
	Failure     *Failure `json:"failure,omitempty"`
}

// WriteJSON atomically replaces path with v: write a sibling, then rename.
func WriteJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(data) > MaxStatusBytes {
		return fmt.Errorf("%s exceeds %d bytes", filepath.Base(path), MaxStatusBytes)
	}
	tmp := path + ".tmp-" + strconv.Itoa(os.Getpid())
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return baseOnly(err)
	}
	// Windows refuses to replace a file another process has open without
	// delete sharing (as os.Open does); readers hold it only briefly.
	deadline := time.Now().Add(renameRetry)
	for {
		err := os.Rename(tmp, path)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			_ = os.Remove(tmp)
			return baseOnly(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

const renameRetry = 2 * time.Second

// ReadJSON strictly decodes a bounded JSON file.
func ReadJSON(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return baseOnly(err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxStatusBytes+1))
	if err != nil {
		return err
	}
	if len(data) > MaxStatusBytes {
		return fmt.Errorf("%s exceeds %d bytes", filepath.Base(path), MaxStatusBytes)
	}
	if err := decodeStrict(data, v); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return nil
}

// baseOnly reduces a file error's path to its base name, keeping its type
// and cause, so test diagnostics do not reveal where a case directory is.
// Full paths stay in the private evidence, not in failure messages.
func baseOnly(err error) error {
	var pe *fs.PathError
	var le *os.LinkError
	switch {
	case errors.As(err, &pe):
		return &fs.PathError{Op: pe.Op, Path: filepath.Base(pe.Path), Err: pe.Err}
	case errors.As(err, &le):
		return &os.LinkError{Op: le.Op, Old: filepath.Base(le.Old), New: filepath.Base(le.New), Err: le.Err}
	}
	return err
}

// SafeFailure formats a failure for public test output: its operation and
// numeric Win32 code, never the raw message, which can name paths or
// accounts.
func SafeFailure(f *Failure) string {
	if f == nil {
		return "none"
	}
	return fmt.Sprintf("%s win32 %d", f.Op, f.Win32)
}
