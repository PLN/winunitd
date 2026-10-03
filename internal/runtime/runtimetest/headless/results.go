package headless

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Result outcomes. Only pass, with confirmed cleanup, counts.
const (
	ResultPass         = "pass"
	ResultFail         = "fail"
	ResultSkip         = "skip"
	ResultInconclusive = "inconclusive"
)

// Record kinds.
const (
	KindPrimary = "primary"
	KindControl = "control"
)

// Token sources: a genuine linger S4U token, a WTS session token, a process
// token (SYSTEM, administrator or standard), a password-bearing logon, or
// the peer machine.
const (
	SourceS4U      = "s4u"
	SourceWTS      = "wts"
	SourceProcess  = "process"
	SourcePassword = "password"
	SourcePeer     = "peer"
)

// SystemSID is LocalSystem.
const SystemSID = "S-1-5-18"

// RecordSchema is the result record schema.
const RecordSchema = 1

// Token is the actual token a record's subject ran under.
type Token struct {
	SID      string `json:"sid,omitempty"`
	Session  uint32 `json:"session"`
	Elevated bool   `json:"elevated"`
	Source   string `json:"source"`
}

// Record is one recorded execution or control, bound to the admitted run.
type Record struct {
	Schema     int    `json:"schema"`
	Key        string `json:"key"`
	Kind       string `json:"kind"`
	Result     string `json:"result"`
	Source     string `json:"source"`
	Admission  string `json:"admission"`
	Executable string `json:"executable"`
	Matrix     string `json:"matrix"`
	Token      *Token `json:"token,omitempty"`
	// ExecutionID names what actually ran; records share one only where the
	// matrix declares a shared execution.
	ExecutionID string `json:"executionId"`
	// Sequence is the run-wide order the driver assigned.
	Sequence int    `json:"sequence"`
	BootID   string `json:"bootId"`
	// PasswordLogons counts password-bearing logons since that boot.
	PasswordLogons   int      `json:"passwordLogons"`
	RunnerID         string   `json:"runnerId,omitempty"`
	CleanupConfirmed bool     `json:"cleanupConfirmed"`
	Controls         []string `json:"controls,omitempty"`
	Evidence         Evidence `json:"evidence"`
	Detail           string   `json:"detail,omitempty"`
}

var sidPattern = regexp.MustCompile(`^S-1-5-[0-9]+(-[0-9]+)*$`)

// Summary is the fail-closed evaluation of records against the matrix.
type Summary struct {
	Matrix            string            `json:"matrix"`
	Source            string            `json:"source"`
	Admission         string            `json:"admission"`
	Required          int               `json:"required"`
	Selected          int               `json:"selected"`
	Executions        int               `json:"executions"`
	Passed            int               `json:"passed"`
	Failed            int               `json:"failed"`
	Incomplete        int               `json:"incomplete"`
	Controls          int               `json:"controls"`
	Missing           []string          `json:"missing,omitempty"`
	Problems          []string          `json:"problems,omitempty"`
	Characterizations map[string]string `json:"characterizations,omitempty"`
	Partial           bool              `json:"partial"`
	Omitted           []string          `json:"omitted,omitempty"`
	// Complete is true only for the whole matrix with every record passed
	// and no problem. Only a complete summary can support #266.
	Complete bool `json:"complete"`
}

type status int

const (
	statusPass status = iota
	statusFail
	statusIncomplete
)

type evaluation struct {
	m        *Matrix
	run      AdmittedRun
	entries  map[string]Entry
	controls map[string]ControlEntry
	records  map[string]Record
	bad      map[string]bool
	roles    map[string]string
	problems []string
}

func (ev *evaluation) problem(format string, args ...any) {
	ev.problems = append(ev.problems, fmt.Sprintf(format, args...))
}

// Summarize evaluates records for one admitted run under a selection.
// Unknown, duplicate, stale or inconsistent records are problems, never
// ignored.
func Summarize(m *Matrix, records []Record, run AdmittedRun, sel Selection) Summary {
	all := m.Expand()
	selected := m.Select(sel)
	s := Summary{Matrix: MatrixHash(), Admission: run.Hash, Required: len(all), Selected: len(selected), Partial: !sel.Empty(),
		Characterizations: map[string]string{}}
	if run.Manifest != nil {
		s.Source = run.Manifest.Source
	}
	ev := &evaluation{m: m, run: run, entries: map[string]Entry{}, controls: map[string]ControlEntry{}, records: map[string]Record{},
		bad: map[string]bool{}, roles: map[string]string{}}
	if run.Manifest == nil || run.Hash == "" {
		ev.problem("no admitted run manifest")
	}
	for _, e := range all {
		ev.entries[e.Key] = e
		for _, c := range e.Controls {
			ev.controls[c.Key] = c
		}
	}
	for _, r := range records {
		if _, dup := ev.records[r.Key]; dup {
			ev.problem("%s: duplicate record", r.Key)
			ev.bad[primaryOf(r.Key)] = true
			continue
		}
		ev.records[r.Key] = r
		if err := ev.check(r); err != nil {
			ev.problem("%s: %v", r.Key, err)
			ev.bad[primaryOf(r.Key)] = true
		}
		if r.Kind == KindControl {
			s.Controls++
		}
	}
	ev.checkRoles()
	ev.checkOrder()
	executions := map[string]bool{}
	for _, r := range ev.records {
		if r.Kind == KindPrimary && r.ExecutionID != "" {
			executions[r.ExecutionID] = true
		}
	}
	s.Executions = len(executions)

	statuses := map[string]status{}
	missing := map[string]bool{}
	for _, e := range all {
		if e.Plane == PlaneReference {
			continue
		}
		st, isMissing, char := ev.evaluate(e)
		statuses[e.Key] = st
		missing[e.Key] = isMissing
		if char != "" {
			s.Characterizations[e.Key] = char
		}
	}
	resolved, _ := resolveAll(all)
	for _, e := range all {
		if e.Plane != PlaneReference {
			continue
		}
		st := statusPass
		for _, k := range resolved[e.Key] {
			st = max(st, statuses[k])
		}
		statuses[e.Key] = st
	}
	if s.Partial {
		in := map[string]bool{}
		for _, e := range selected {
			in[e.Key] = true
		}
		for _, e := range all {
			if !in[e.Key] {
				s.Omitted = append(s.Omitted, e.Key)
			}
		}
	}
	for _, e := range selected {
		switch {
		case missing[e.Key]:
			s.Missing = append(s.Missing, e.Key)
			s.Incomplete++
		case statuses[e.Key] == statusPass:
			s.Passed++
		case statuses[e.Key] == statusFail:
			s.Failed++
		default:
			s.Incomplete++
		}
	}
	sort.Strings(ev.problems)
	s.Problems = ev.problems
	if s.Partial {
		// A development selection reports the problems of what it selected
		// and those that belong to no record.
		in := map[string]bool{}
		for _, e := range selected {
			in[e.Key] = true
		}
		s.Problems = slices.DeleteFunc(s.Problems, func(p string) bool {
			key, _, _ := strings.Cut(p, ":")
			_, isEntry := ev.entries[primaryOf(key)]
			return isEntry && !in[primaryOf(key)]
		})
	}
	s.Complete = !s.Partial && s.Passed == s.Required && len(s.Problems) == 0
	return s
}

// check validates one record's shape and binding.
func (ev *evaluation) check(r Record) error {
	if r.Schema != RecordSchema {
		return fmt.Errorf("schema %d", r.Schema)
	}
	switch r.Result {
	case ResultPass, ResultFail, ResultSkip, ResultInconclusive:
	default:
		return fmt.Errorf("result %q", r.Result)
	}
	if ev.run.Manifest == nil || r.Source != ev.run.Manifest.Source || r.Admission != ev.run.Hash {
		return errors.New("not recorded for the admitted run")
	}
	if r.Executable == "" || r.Executable != ev.run.Manifest.Lookup(workloadImage) {
		return errors.New("not made by the admitted " + workloadImage)
	}
	if r.Matrix != MatrixHash() {
		return errors.New("recorded against another matrix")
	}
	if r.Sequence < 1 || r.BootID == "" || r.PasswordLogons < 0 {
		return errors.New("no run order or boot")
	}
	switch r.Kind {
	case KindPrimary:
		e, ok := ev.entries[r.Key]
		if !ok || e.Plane == PlaneReference {
			return errors.New("not an executed record of this matrix")
		}
		if e.Plane == PlaneOwnerTest && r.RunnerID == "" || e.Plane == PlaneDaemon && r.RunnerID != "" {
			return errors.New("runner identity does not match the plane")
		}
		if r.ExecutionID == "" {
			return errors.New("no execution")
		}
		for _, c := range r.Controls {
			if _, ok := ev.controls[c]; !ok || primaryOf(c) != r.Key {
				return fmt.Errorf("links unknown control %q", c)
			}
		}
	case KindControl:
		if _, ok := ev.controls[r.Key]; !ok {
			return errors.New("not a control of this matrix")
		}
		if len(r.Controls) > 0 || r.RunnerID != "" {
			return errors.New("a control links nothing")
		}
	default:
		return fmt.Errorf("kind %q", r.Kind)
	}
	if r.Token != nil && r.Token.Source != SourcePeer && !sidPattern.MatchString(r.Token.SID) {
		return errors.New("token without an account SID")
	}
	observed := r.Kind == KindPrimary && ev.entries[r.Key].Observe != nil || r.Kind == KindControl && ev.controls[r.Key].Observe != nil
	if r.Evidence.Observer != nil && !observed {
		return errors.New("carries an observer report its case does not use")
	}
	return nil
}

// accountOf names the role a record's token must belong to.
func (ev *evaluation) accountOf(key string) (string, string) {
	if c, ok := ev.controls[key]; ok {
		return c.Account, c.Mode
	}
	e := ev.entries[key]
	return e.Account, e.Mode
}

// checkRoles requires one SID per role, distinct roles and no SYSTEM role.
func (ev *evaluation) checkRoles() {
	keys := make([]string, 0, len(ev.records))
	for k := range ev.records {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		r := ev.records[k]
		account, mode := ev.accountOf(k)
		if r.Token == nil || mode == ModePeer || mode == ModeSystem || account == "" || account == AccountSystem {
			continue
		}
		if prev, ok := ev.roles[account]; ok && prev != r.Token.SID {
			ev.problem("%s: account %s ran under another SID than its other records", k, account)
			ev.bad[primaryOf(k)] = true
			continue
		}
		ev.roles[account] = r.Token.SID
	}
	seen := map[string]string{}
	for role, sid := range ev.roles {
		if sid == SystemSID {
			ev.problem("account %s is SYSTEM", role)
		}
		if other, dup := seen[sid]; dup {
			ev.problem("accounts %s and %s share one SID", role, other)
		}
		seen[sid] = role
	}
}

// tokenFits checks a token against its record's account role and mode.
func (ev *evaluation) tokenFits(t *Token, account, mode string) error {
	if t == nil {
		return errors.New("no token context")
	}
	switch mode {
	case ModeSystem:
		if t.SID != SystemSID {
			return errors.New("not SYSTEM")
		}
	case ModeS4U:
		if t.Source != SourceS4U || t.Session != 0 || t.Elevated || t.SID == SystemSID {
			return errors.New("not a genuine session-zero S4U standard token")
		}
	case ModeWTS:
		if t.Source != SourceWTS || t.Session == 0 || t.Elevated || t.SID == SystemSID {
			return errors.New("not a standard interactive session token")
		}
	case ModeFilteredAdmin:
		if t.Source != SourceProcess || t.Elevated || t.SID == SystemSID {
			return errors.New("not a filtered administrator token")
		}
	case ModePassword:
		if t.Source != SourcePassword || t.SID == SystemSID {
			return errors.New("not a password-bearing logon")
		}
	case ModePeer:
		if t.Source != SourcePeer {
			return errors.New("not the peer's record")
		}
		return nil
	default:
		return fmt.Errorf("mode %q", mode)
	}
	if want, ok := ev.roles[account]; ok && account != AccountSystem && t.SID != want {
		return fmt.Errorf("not account %s", account)
	}
	return nil
}

// evaluate judges one executed entry. It returns whether the record is
// missing and, for characterization rows, the characterization.
func (ev *evaluation) evaluate(e Entry) (status, bool, string) {
	r, ok := ev.records[e.Key]
	if !ok {
		return statusIncomplete, true, ""
	}
	if ev.bad[e.Key] {
		return statusIncomplete, false, ""
	}
	problem := func(format string, args ...any) { ev.problem(e.Key+": "+format, args...) }
	if r.Kind != KindPrimary {
		problem("recorded as a %s", r.Kind)
		return statusIncomplete, false, ""
	}
	switch r.Result {
	case ResultFail:
		return statusFail, false, ""
	case ResultPass:
	default:
		return statusIncomplete, false, ""
	}
	st := statusPass
	open := func(format string, args ...any) {
		problem(format, args...)
		st = max(st, statusIncomplete)
	}
	if !r.CleanupConfirmed {
		open("passed without confirmed cleanup")
	}
	if err := ev.tokenFits(r.Token, e.Account, e.Mode); err != nil {
		open("token: %v", err)
	}
	if err := headerMatches(r.Token, r.Evidence.Token); err != nil {
		open("%v", err)
	}
	if e.Mode == ModeFilteredAdmin && !FilteredAdministrator(r.Evidence.Token) {
		open("the token probe is not an administrator's filtered token")
	}
	sid, peer := ev.roles[e.Account], ev.peerOf(e.Account)
	var life Lifecycle
	switch e.Proof {
	case ProofPending:
		open("no qualified producer of this case's proof exists yet")
	case ProofObserver:
		life = ev.lifecycle(e.Observe, e.Account, e.Mode, r, open)
	case ProofProbe:
		life = ev.lifecycle(e.Observe, e.Account, e.Mode, r, open)
		for _, p := range CheckSubject(r.Evidence.Observer, r.Evidence.Token, e.Account, e.Mode) {
			open("%s", p)
		}
	case ProofNamedTest:
		for _, p := range CheckTestRun(r.Evidence.TestRun, e, r.RunnerID, sid, ev.run) {
			open("%s", p)
		}
	default:
		open("proof %q", e.Proof)
	}
	for _, f := range meets(e.Requires, metrics(life, r.Evidence.Status, e.CapSec)) {
		open("%s", f)
	}
	if e.Paths != "" {
		for _, p := range CheckPaths(e.Paths, r.Evidence.Paths) {
			open("path %s", p)
		}
	}
	if e.Pipe != "" {
		for _, p := range CheckPipe(e.Pipe, r.Evidence.Pipe) {
			open("pipe %s", p)
		}
		for _, p := range CheckPipeClients(e.Pipe, r.Evidence.Pipe, r.Evidence.PipeServers, sid, peer, e.Account, r.Evidence.Observer) {
			open("pipe %s", p)
		}
	}
	controls := map[string]Record{}
	for _, c := range e.Controls {
		if !slices.Contains(r.Controls, c.Key) {
			open("required control %s is not linked", c.Name)
			continue
		}
		cr, found := ev.records[c.Key]
		switch {
		case !found || ev.bad[e.Key]:
			open("control %s is missing", c.Name)
		case cr.Result != ResultPass:
			open("control %s did not pass", c.Name)
		case !cr.CleanupConfirmed:
			open("control %s passed without confirmed cleanup", c.Name)
		default:
			copen := func(format string, args ...any) { open("control "+c.Name+" "+format, args...) }
			if ev.control(e, r, c, cr, copen) {
				controls[c.Name] = cr
			}
		}
	}
	char := ""
	if e.Characterize != "" {
		var why string
		switch e.Characterize {
		case CharTCP:
			var receipt *EchoReceipt
			if c, ok := controls["peer-receipt"]; ok {
				receipt = c.Evidence.Receipt
			}
			char, why = CharacterizeTCP(r.Evidence.TCP, receipt)
		case CharSMB:
			var password *SMBResult
			if c, ok := controls["password-share"]; ok {
				password = c.Evidence.SMB
			}
			var server *Principal
			if c, ok := controls["server-principal"]; ok {
				server = c.Evidence.Server
			}
			char, why = CharacterizeSMB(r.Evidence.SMB, password, server, sid)
		case CharEFS:
			var password *EFSResult
			if c, ok := controls["password-decrypt"]; ok {
				password = c.Evidence.EFS
			}
			char, why = CharacterizeEFS(r.Evidence.EFS, password)
		}
		switch char {
		case Succeeded, Refused:
		case Failed:
			problem("characterization failed: %s", why)
			st = statusFail
		default:
			open("characterization %s: %s", char, why)
		}
	}
	return st, false, char
}

// control judges one control record of entry e by its declared proof and
// reports whether it holds.
func (ev *evaluation) control(e Entry, r Record, c ControlEntry, cr Record, open func(string, ...any)) bool {
	bad := false
	fail := func(format string, args ...any) {
		open(format, args...)
		bad = true
	}
	if err := ev.tokenFits(cr.Token, c.Account, c.Mode); err != nil {
		fail("token: %v", err)
	}
	if err := headerMatches(cr.Token, cr.Evidence.Token); err != nil {
		fail("%v", err)
	}
	sid := ev.roles[e.Account]
	var life Lifecycle
	switch c.Proof {
	case ProofPending:
		fail("has no qualified producer yet")
	case ProofFirstUse:
		for _, p := range CheckFirstUse(cr.Evidence.FirstUse, sid, r.Evidence.Observer) {
			fail("%s", p)
		}
		// The check is recorded on the boot it ran on, never relabelled
		// with the cold boot that follows it.
		if f := cr.Evidence.FirstUse; f != nil && cr.BootID != f.Boot.String() {
			fail("is recorded on another boot than it ran on")
		}
	case ProofReceipt:
		if cr.Evidence.Receipt == nil {
			fail("has no receipt")
		}
	case ProofPrincipal:
		if cr.Evidence.Server == nil {
			fail("has no server attribution")
		}
	case ProofPassword:
		if cr.Evidence.SMB == nil && cr.Evidence.EFS == nil || !PasswordToken(cr.Evidence.Token, sid) {
			fail("is not the account's password-bearing probe")
		}
	case ProofEndpoint:
		for _, p := range CheckEndpoints(cr.Evidence.Endpoints, ev.peerOf(e.Account)) {
			fail("%s", p)
		}
	case ProofObserver:
		life = ev.lifecycle(c.Observe, e.Account, e.Mode, cr, fail)
	case ProofProbe:
		life = ev.lifecycle(c.Observe, e.Account, e.Mode, cr, fail)
		for _, p := range CheckSubject(cr.Evidence.Observer, cr.Evidence.Token, e.Account, e.Mode) {
			fail("%s", p)
		}
		if c.Paths == "" {
			fail("names no path set")
		}
		for _, p := range CheckPaths(c.Paths, cr.Evidence.Paths) {
			fail("path %s", p)
		}
	default:
		fail("proof %q", c.Proof)
	}
	for _, f := range meets(c.Requires, metrics(life, cr.Evidence.Status, 0)) {
		fail("%s", f)
	}
	return !bad
}

// peerOf is the other account's SID, if its records named one.
func (ev *evaluation) peerOf(account string) string {
	switch account {
	case AccountA:
		return ev.roles[AccountB]
	case AccountB:
		return ev.roles[AccountA]
	}
	return ""
}

// lifecycle derives an observed record's lifecycle from the observer report
// it carries: made by an admitted observer on the record's boot, watching
// the record's account under its SID and mode.
func (ev *evaluation) lifecycle(spec *ObserveSpec, account, mode string, r Record, open func(string, ...any)) Lifecycle {
	rep := r.Evidence.Observer
	if spec == nil || rep == nil {
		open("no observer report")
		return Lifecycle{}
	}
	if ev.run.Manifest.Lookup(workloadImage) == "" || rep.Executable != ev.run.Manifest.Lookup(workloadImage) {
		open("observer executable is not the admitted %s", workloadImage)
	}
	if rep.Boot.String() != r.BootID {
		open("observer report is from another boot")
	}
	sid := ""
	if r.Token != nil {
		sid = r.Token.SID
	}
	life, problems := DeriveLifecycle(rep, *spec, account, sid, mode)
	for _, p := range problems {
		open("observer: %s", p)
	}
	return life
}

// checkOrder enforces phase order, the fresh-boot phase, controls that must
// immediately precede their record, declared shared executions and runners.
func (ev *evaluation) checkOrder() {
	type placed struct {
		key, account string
		phase, seq   int
		boot         string
		passwords    int
		// preboot marks a check that runs before its record's boot, such
		// as the first-use check; its own proof binds it to that boot.
		preboot bool
		// observed records carry an observer report, whose logon sessions
		// count password-bearing logons since the boot.
		observed bool
		primary  bool
	}
	var all []placed
	for k, r := range ev.records {
		if ev.bad[primaryOf(k)] {
			continue
		}
		p := placed{key: k, seq: r.Sequence, boot: r.BootID, passwords: r.PasswordLogons, observed: r.Evidence.Observer != nil}
		if rep := r.Evidence.Observer; rep != nil {
			p.passwords = max(p.passwords, rep.PasswordLogonsSince())
		}
		if c, ok := ev.controls[k]; ok {
			p.phase, p.account, p.preboot = c.Phase, c.Account, c.ImmediatelyBefore
		} else {
			p.primary = true
			e := ev.entries[k]
			p.phase, p.account = e.Phase, e.Account
		}
		all = append(all, p)
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].seq < all[j].seq || all[i].seq == all[j].seq && all[i].key < all[j].key
	})
	// Phase order: a record of a later phase never precedes an earlier one.
	highest := 0
	for _, p := range all {
		if p.phase < highest {
			ev.problem("%s: phase %d record after a phase %d record", p.key, p.phase, highest)
		}
		highest = max(highest, p.phase)
	}
	for _, ph := range ev.m.Phases {
		boot := ""
		for _, p := range all {
			if p.phase != ph.ID || p.preboot {
				continue
			}
			if ph.OneBoot && boot != "" && p.boot != boot {
				ev.problem("%s: phase %s spans more than one boot", p.key, ph.Name)
			}
			boot = p.boot
			if ph.NoPassword && p.passwords != 0 {
				ev.problem("%s: phase %s ran after a password-bearing logon", p.key, ph.Name)
			}
			if ph.NoPassword && p.primary && !p.observed && ev.entries[p.key].Proof != ProofPending {
				ev.problem("%s: phase %s has no observed logon sessions", p.key, ph.Name)
			}
		}
	}
	// Sequences are unique except within one shared execution.
	seqs := map[int]string{}
	for _, p := range all {
		if prev, dup := seqs[p.seq]; dup && !ev.sameExecution(prev, p.key) {
			ev.problem("%s and %s share sequence %d", prev, p.key, p.seq)
		}
		seqs[p.seq] = p.key
	}
	for key, c := range ev.controls {
		if !c.ImmediatelyBefore {
			continue
		}
		cr, okC := ev.records[key]
		pr, okP := ev.records[primaryOf(key)]
		if !okC || !okP {
			continue
		}
		if cr.Sequence >= pr.Sequence {
			ev.problem("%s: does not precede its record", key)
			continue
		}
		account := ev.entries[primaryOf(key)].Account
		for _, p := range all {
			if p.seq > cr.Sequence && p.seq < pr.Sequence && p.account == account {
				ev.problem("%s: %s ran between the control and its record", key, p.key)
			}
		}
	}
	ev.checkExecutions()
	ev.checkRunners()
}

func (ev *evaluation) sameExecution(a, b string) bool {
	ea, eb := ev.entries[primaryOf(a)], ev.entries[primaryOf(b)]
	return a != b && !strings.Contains(a, "#") && !strings.Contains(b, "#") && ea.Execution != "" && ea.Execution == eb.Execution
}

// checkExecutions requires each declared shared execution to have one ID
// and every other executed record its own.
func (ev *evaluation) checkExecutions() {
	byID := map[string][]string{}
	groupIDs := map[string]string{}
	groupReports := map[string]string{}
	for k, r := range ev.records {
		if r.Kind != KindPrimary || ev.bad[k] {
			continue
		}
		byID[r.ExecutionID] = append(byID[r.ExecutionID], k)
		if g := ev.entries[k].Execution; g != "" {
			if prev, ok := groupIDs[g]; ok && prev != r.ExecutionID {
				ev.problem("%s: shared execution %s recorded under two IDs", k, g)
			}
			groupIDs[g] = r.ExecutionID
			report, _ := json.Marshal(r.Evidence.Observer)
			if prev, ok := groupReports[g]; ok && prev != string(report) {
				ev.problem("%s: shared execution %s has two observer reports", k, g)
			}
			groupReports[g] = string(report)
		}
	}
	for id, keys := range byID {
		if len(keys) < 2 {
			continue
		}
		sort.Strings(keys)
		g := ev.entries[keys[0]].Execution
		for _, k := range keys[1:] {
			if g == "" || ev.entries[k].Execution != g {
				ev.problem("%s and %s claim one execution %s", keys[0], k, id)
				break
			}
		}
	}
}

// checkRunners requires each repetition of a runner case in its own fresh
// runner, with every variant of that repetition in the same runner.
func (ev *evaluation) checkRunners() {
	for id, c := range ev.m.Cases {
		if c.Runners == 0 {
			continue
		}
		runners := map[string]string{}
		for rep := 1; rep <= c.Runners; rep++ {
			runner := ""
			for _, v := range c.Variants {
				key := fmt.Sprintf("%s/%s/r%d", id, v.ID, rep)
				r, ok := ev.records[key]
				if !ok {
					continue
				}
				if runner != "" && r.RunnerID != runner {
					ev.problem("%s: repetition r%d spans two runners", key, rep)
				}
				runner = r.RunnerID
			}
			if runner == "" {
				continue
			}
			if prev, dup := runners[runner]; dup {
				ev.problem("%s: repetitions %s and r%d share runner", id, prev, rep)
			}
			runners[runner] = fmt.Sprintf("r%d", rep)
		}
	}
}

// ResultFileName is a filesystem-safe name for a record key.
func ResultFileName(key string) string {
	return strings.NewReplacer("/", "_", "#", "+").Replace(key) + ".json"
}

// WriteRecord stores r in dir, refusing to replace an existing record.
func WriteRecord(dir string, r Record) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, ResultFileName(r.Key)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return baseOnly(err)
	}
	_, werr := f.Write(append(data, '\n'))
	return errors.Join(werr, f.Close())
}

// ReadRecords strictly decodes every *.json record in dir, in name order.
func ReadRecords(dir string) ([]Record, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, baseOnly(err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) > 4096 {
		return nil, errors.New("more than 4096 records")
	}
	var out []Record
	for _, name := range names {
		data, err := readBounded(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		var r Record
		if err := decodeStrict(data, &r); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if ResultFileName(r.Key) != name {
			return nil, fmt.Errorf("%s holds record %s", name, r.Key)
		}
		out = append(out, r)
	}
	return out, nil
}
