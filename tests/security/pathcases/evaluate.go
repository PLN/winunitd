package pathcases

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// ObservationSchema is the observation format version.
const ObservationSchema = 2

// systemSID is LocalSystem.
const systemSID = "S-1-5-18"

// Token facts the actor and performer checks use.
const (
	elevationTypeDefault = 1
	elevationTypeFull    = 2
	elevationTypeLimited = 3
	groupEnabled         = 0x4
	groupUseForDenyOnly  = 0x10
	productTokenSource   = "winunitd"
)

// Reparse tags and access rights the object checks use.
const (
	tagMountPoint   = 0xa0000003
	tagSymlink      = 0xa000000c
	rightDeleteKid  = 0x40
	rightDelete     = 0x10000
	accessDenied    = 5
	stageBaseline   = "baseline"
	stageBefore     = "before"
	stageStepsBegin = "steps-begin"
	stageStepsEnd   = "steps-end"
	stageAfter      = "after"
	stageRestored   = "restored"
)

// stageOrder is the order an execution's stage marks must have; object
// states are read at baseline, before, after and restored.
var (
	stageOrder  = []string{stageBaseline, stageBefore, stageStepsBegin, stageStepsEnd, stageAfter, stageRestored}
	stateStages = []string{stageBaseline, stageBefore, stageAfter, stageRestored}
	accountSID  = regexp.MustCompile(`^S-1-5-21-\d+-\d+-\d+-\d+$`)
	fullCommit  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sha256Hex   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	runID       = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	objectKinds = []string{"file", "directory", "junction", "symlink-file", "symlink-directory"}
)

// Run is what the admitted run fixes for an evaluation: its source commit
// and admission manifest hash, and the SID each account role must be:
// standard, filtered-admin, user and peer.
type Run struct {
	Source    string
	Admission string
	Accounts  map[string]string
}

// Observation is one native execution of a case.
type Observation struct {
	Schema int `json:"schema"`
	// Ledger is the digest of the ledger the case was evaluated from.
	Ledger   string   `json:"ledger"`
	Case     string   `json:"case"`
	Envelope Envelope `json:"envelope"`
	// Actor is the actor's own process and token; absent for a
	// fixture-made state.
	Actor *Process `json:"actor,omitempty"`
	// States are each role's object state at each stage.
	States map[string]map[string]ObjectState `json:"states"`
	Steps  []StepResult                      `json:"steps"`
}

// Envelope binds an observation to its admitted run: the source commit,
// the admission manifest's hash, the run and execution IDs, the SYSTEM
// observer that recorded it and the times its stages began.
type Envelope struct {
	Source    string      `json:"source"`
	Admission string      `json:"admission"`
	Run       string      `json:"run"`
	Execution string      `json:"execution"`
	Observer  Process     `json:"observer"`
	Stages    []StageMark `json:"stages"`
}

// StageMark is when a stage began.
type StageMark struct {
	Name string `json:"name"`
	At   uint64 `json:"at"`
}

// Process is a held process incarnation and its primary token.
type Process struct {
	PID     uint32     `json:"pid"`
	Created uint64     `json:"created"`
	Token   TokenFacts `json:"token"`
}

// TokenFacts is a token as the native producer read it.
type TokenFacts struct {
	SID              string `json:"sid"`
	Session          uint32 `json:"session"`
	Elevated         bool   `json:"elevated"`
	ElevationType    uint32 `json:"elevationType"`
	AdminFlags       uint32 `json:"adminFlags"`
	Integrity        string `json:"integrity"`
	Source           string `json:"source"`
	LogonType        uint32 `json:"logonType"`
	AuthenticationID string `json:"authenticationId"`
}

// Performer is the process that performed a step and, when it
// impersonated, the effective token it acted with.
type Performer struct {
	Process
	Impersonated bool        `json:"impersonated"`
	Effective    *TokenFacts `json:"effective,omitempty"`
}

// ObjectState is a path as the SYSTEM observer read it without following a
// reparse point. Read says the inspection succeeded; an object that does
// not exist is read and absent. An existing one has its kind, file ID,
// owner and DACL; a file or directory its content witness (the bytes', or
// the listing's, SHA-256); a link its reparse tag and the file ID of the
// object it resolves to; and, where a case measures them, the actor's
// effective rights on it.
type ObjectState struct {
	At          uint64  `json:"at"`
	Read        bool    `json:"read"`
	Exists      bool    `json:"exists"`
	Kind        string  `json:"kind,omitempty"`
	FileID      string  `json:"fileId,omitempty"`
	Owner       string  `json:"owner,omitempty"`
	DACL        string  `json:"dacl,omitempty"`
	Content     string  `json:"content,omitempty"`
	ReparseTag  uint32  `json:"reparseTag,omitempty"`
	ResolvedID  string  `json:"resolvedId,omitempty"`
	ActorRights *uint32 `json:"actorRights,omitempty"`
}

// identity is what must stay the same for an object to be the same one.
func (s ObjectState) identity() ObjectState {
	s.At, s.ActorRights = 0, nil
	return s
}

// StepResult is one step's observed result: who performed it and when;
// whether it succeeded; for a failure its error domain (win32, msi or
// product), code and the product's diagnostic; whether the consumer's or
// operation's own result exposed the target; and the consumer's own result.
type StepResult struct {
	Name          string     `json:"name"`
	At            uint64     `json:"at"`
	Performer     *Performer `json:"performer"`
	Outcome       string     `json:"outcome"`
	Domain        string     `json:"domain,omitempty"`
	Code          uint32     `json:"code,omitempty"`
	Diagnostic    string     `json:"diagnostic,omitempty"`
	TargetExposed *bool      `json:"targetExposed"`
	Witness       string     `json:"witness,omitempty"`
}

// Verdict is a case's recomputed result.
type Verdict struct {
	Case     string   `json:"case"`
	Pass     bool     `json:"pass"`
	Problems []string `json:"problems,omitempty"`
}

// Evaluate recomputes a case's verdict from one observation of the given
// ledger in the given admitted run. A case awaiting a policy decision never
// passes, whatever was observed.
func Evaluate(l *Ledger, run Run, o *Observation) Verdict {
	v := Verdict{Case: o.Case}
	add := func(format string, args ...any) { v.Problems = append(v.Problems, fmt.Sprintf(format, args...)) }
	c, ok := l.Case(o.Case)
	switch {
	case o.Schema != ObservationSchema:
		add("observation schema %d", o.Schema)
	case !ok:
		add("no such case")
	case o.Ledger == "" || o.Ledger != l.Digest():
		add("the observation names another ledger")
	}
	if !ok {
		return v
	}
	if c.Outcome == OutcomeDecisionNeeded {
		add("the case awaits a product policy decision")
	}
	marks := envelopeProblems(run, &o.Envelope, add)
	for _, p := range actorProblems(c, run, o.Actor) {
		add("%s", p)
	}
	statesProblems(c, o, marks, add)
	stepsProblems(c, run, o, marks, add)
	v.Pass = len(v.Problems) == 0
	return v
}

// envelopeProblems checks the envelope against the admitted run and
// returns the stage marks by name.
func envelopeProblems(run Run, e *Envelope, add func(string, ...any)) map[string]uint64 {
	if !fullCommit.MatchString(e.Source) || e.Source != run.Source || !sha256Hex.MatchString(e.Admission) || e.Admission != run.Admission {
		add("the observation is not of the admitted run's source and artifacts")
	}
	if !runID.MatchString(e.Run) || !runID.MatchString(e.Execution) {
		add("the observation names no run and execution")
	}
	ob := e.Observer
	if ob.PID == 0 || ob.Created == 0 || ob.Token.SID != systemSID || ob.Token.Session != 0 {
		add("the observer is not a SYSTEM process in session zero")
	}
	marks := map[string]uint64{}
	ordered := len(e.Stages) == len(stageOrder)
	for i, m := range e.Stages {
		if !ordered || m.Name != stageOrder[i] || m.At == 0 || i > 0 && m.At <= e.Stages[i-1].At {
			ordered = false
			break
		}
		marks[m.Name] = m.At
	}
	if !ordered {
		add("the stage marks are missing or out of order")
		return nil
	}
	return marks
}

// interactiveLogon reports an interactive, remote interactive or cached
// interactive logon type.
func interactiveLogon(t uint32) bool { return t == 2 || t == 10 || t == 11 }

// tokenClassProblem checks a token against an actor class and mode.
func tokenClassProblem(actor, mode string, t TokenFacts) string {
	if t.Elevated || t.AuthenticationID == "" {
		return "the " + actor + " token is elevated or has no logon"
	}
	switch actor {
	case ActorStandard:
		if t.Session == 0 || !interactiveLogon(t.LogonType) || t.ElevationType != elevationTypeDefault || t.AdminFlags != 0 || t.Integrity != "medium" {
			return "the actor is not a standard interactive caller"
		}
	case ActorFilteredAdmin:
		if t.Session == 0 || !interactiveLogon(t.LogonType) || t.ElevationType != elevationTypeLimited || t.AdminFlags&groupUseForDenyOnly == 0 ||
			t.AdminFlags&groupEnabled != 0 || t.Integrity != "medium" {
			return "the actor is not a genuine UAC-filtered administrator"
		}
	case ActorUser:
		switch mode {
		case "s4u":
			if t.Session != 0 || t.Source != productTokenSource || t.LogonType != 3 && t.LogonType != 4 {
				return "the account's context is not the product's S4U logon"
			}
		default:
			if t.Session == 0 || !interactiveLogon(t.LogonType) || t.ElevationType == elevationTypeFull || t.AdminFlags&groupEnabled != 0 {
				return "the account's context is not a non-elevated interactive logon"
			}
		}
	}
	return ""
}

// actorProblems binds the actor to the run's account for its class and to
// the genuine token of its mode.
func actorProblems(c Case, run Run, a *Process) []string {
	if c.Actor == ActorSystemFixture {
		if a != nil && a.Token.SID != systemSID {
			return []string{"the fixture is not SYSTEM"}
		}
		return nil
	}
	want := run.Accounts[c.Actor]
	switch {
	case !accountSID.MatchString(want):
		return []string{"the run names no " + c.Actor + " account"}
	case a == nil || a.PID == 0 || a.Created == 0:
		return []string{"no held actor process"}
	case a.Token.SID != want:
		return []string{"the actor is not the run's " + c.Actor + " account"}
	}
	if p := tokenClassProblem(c.Actor, c.Mode, a.Token); p != "" {
		return []string{p}
	}
	return nil
}

// statesProblems requires every role's object to have been read at every
// stage, with its mandatory facts; the link absent before the fixture made
// it and after restoration, of the shape's exact kind and tag, and
// resolving to the watched target; the target, leaf, ancestor and link
// unchanged by the steps; the sibling present; restoration to the
// baseline; and, for an ancestor case, no measured right of the actor to
// remove or rename the leaf.
func statesProblems(c Case, o *Observation, marks map[string]uint64, add func(string, ...any)) {
	for _, stage := range stateStages {
		roles := o.States[stage]
		for _, role := range c.Roles {
			s, ok := roles[role]
			if !ok || !s.Read {
				add("the %s was not read at %s", role, stage)
				continue
			}
			if marks != nil && (s.At < marks[stage] || stage != stageRestored && s.At >= marks[nextStage(stage)]) {
				add("the %s was read outside its %s stage", role, stage)
			}
			linkAbsent := role == RoleLink && (stage == stageBaseline || stage == stageRestored)
			if linkAbsent {
				if s.Exists {
					add("the link exists at %s", stage)
				}
				continue
			}
			if p := objectProblem(c, role, s); p != "" {
				add("the %s at %s: %s", role, stage, p)
			}
			if role == RoleLink && s.ResolvedID != roles[RoleTarget].FileID {
				add("the link at %s does not resolve to the watched target", stage)
			}
		}
	}
	before, after := o.States[stageBefore], o.States[stageAfter]
	for _, role := range []string{RoleTarget, RoleLeaf, RoleAncestor, RoleLink} {
		if slices.Contains(c.Roles, role) && before[role].identity() != after[role].identity() {
			add("the %s changed during the steps", role)
		}
	}
	for _, role := range c.Roles {
		if o.States[stageRestored][role].identity() != o.States[stageBaseline][role].identity() {
			add("the %s was not restored to its baseline", role)
		}
	}
	if c.Shape == "ancestor-rename" {
		anc, leaf := before[RoleAncestor].ActorRights, before[RoleLeaf].ActorRights
		switch {
		case anc == nil || leaf == nil:
			add("the actor's effective rights on the ancestor and leaf were not measured")
		case *anc&rightDeleteKid != 0 || *leaf&rightDelete != 0:
			add("the actor holds a right to remove or rename the leaf")
		}
	}
}

func nextStage(stage string) string {
	i := slices.Index(stageOrder, stage)
	if i < 0 || i+1 >= len(stageOrder) {
		return stage
	}
	return stageOrder[i+1]
}

// objectProblem checks an existing object's mandatory facts for its role
// and the case's shape.
func objectProblem(c Case, role string, s ObjectState) string {
	if !s.Exists {
		return "missing"
	}
	if !slices.Contains(objectKinds, s.Kind) || s.FileID == "" || s.Owner == "" || s.DACL == "" {
		return "no kind, file ID, owner or DACL"
	}
	if role != RoleLink {
		if s.Kind != c.RoleKinds[role] || s.ReparseTag != 0 || !sha256Hex.MatchString(s.Content) {
			return "not the case's " + c.RoleKinds[role] + " with a content witness"
		}
		return ""
	}
	switch c.Shape {
	case "descendant-junction", "component-junction":
		if s.Kind != "junction" || s.ReparseTag != tagMountPoint {
			return "not a junction"
		}
	case "descendant-file-link":
		if s.Kind != "symlink-file" || s.ReparseTag != tagSymlink {
			return "not a file symbolic link"
		}
	}
	if s.ResolvedID == "" {
		return "its resolution was not read"
	}
	return ""
}

// stepsProblems requires every step of the case, once, performed in its
// context by the bound process, within the steps stage, with a coherent
// result of the expected kind, the consumer's own expected result and no
// exposure of the target.
func stepsProblems(c Case, run Run, o *Observation, marks map[string]uint64, add func(string, ...any)) {
	results := map[string]StepResult{}
	for _, r := range o.Steps {
		if _, dup := results[r.Name]; dup {
			add("step %s recorded twice", r.Name)
		}
		results[r.Name] = r
	}
	for _, s := range c.Steps {
		r, found := results[s.Name]
		if !found {
			add("step %s was not run", s.Name)
			continue
		}
		delete(results, s.Name)
		if marks != nil && (r.At < marks[stageStepsBegin] || r.At > marks[stageStepsEnd]) {
			add("step %s ran outside the steps stage", s.Name)
		}
		if p := performerProblem(c, run, o.Actor, s.Context, r.Performer); p != "" {
			add("step %s: %s", s.Name, p)
		}
		if p := resultProblem(c, s, r); p != "" {
			add("step %s: %s", s.Name, p)
		}
	}
	for name := range results {
		add("step %s is not in the case", name)
	}
}

// performerProblem binds a step to who performed it: the actor's own held
// process; a SYSTEM process; or a SYSTEM process impersonating the
// account, with the actor's logon, or the run's peer account.
func performerProblem(c Case, run Run, actor *Process, context string, p *Performer) string {
	if p == nil || p.PID == 0 || p.Created == 0 || p.Token.SID == "" {
		return "no held performer"
	}
	switch context {
	case ContextActor:
		if actor == nil || p.PID != actor.PID || p.Created != actor.Created || p.Token != actor.Token || p.Impersonated {
			return "not performed by the actor's own process"
		}
	case ContextSystem:
		if p.Token.SID != systemSID || p.Impersonated {
			return "not performed by SYSTEM"
		}
	case ContextUserImpersonated, ContextPeerImpersonated:
		if p.Token.SID != systemSID || !p.Impersonated || p.Effective == nil {
			return "not performed by SYSTEM impersonating an account"
		}
		e := p.Effective
		if context == ContextUserImpersonated {
			if actor == nil || e.SID != run.Accounts[ActorUser] || e.SID != actor.Token.SID || e.AuthenticationID != actor.Token.AuthenticationID {
				return "not impersonating the account's own logon"
			}
			if tp := tokenClassProblem(ActorUser, c.Mode, *e); tp != "" {
				return tp
			}
		} else {
			peer := run.Accounts["peer"]
			if !accountSID.MatchString(peer) || e.SID != peer || e.SID == run.Accounts[ActorUser] {
				return "not impersonating the run's peer account"
			}
			if tp := tokenClassProblem(ActorUser, "wts", *e); tp != "" {
				return tp
			}
		}
	}
	return ""
}

// resultProblem checks a step result's coherence and expectation. A
// success carries no error; a failure names its domain, and a win32 or msi
// failure its code, a product failure its diagnostic. Any exposure of the
// target fails, even on a denied operation.
func resultProblem(c Case, s Step, r StepResult) string {
	switch r.Outcome {
	case "succeeded":
		if r.Domain != "" || r.Code != 0 || r.Diagnostic != "" {
			return "a success carries an error"
		}
	case "failed":
		switch r.Domain {
		case "win32", "msi":
			if r.Code == 0 {
				return "a failure has no code"
			}
		case "product":
			if r.Diagnostic == "" {
				return "a product refusal has no diagnostic"
			}
		default:
			return "a failure has no error domain"
		}
	default:
		return "no outcome"
	}
	if r.TargetExposed == nil {
		return "whether the target was exposed is unknown"
	}
	if *r.TargetExposed {
		return "the target was exposed"
	}
	if r.Witness != s.Witness {
		return fmt.Sprintf("the consumer's result is %q, not %q", r.Witness, s.Witness)
	}
	ok := r.Outcome == "succeeded"
	switch s.Expect {
	case ExpectOK, ExpectNoFollow:
		if !ok {
			return "expected " + s.Expect
		}
	case ExpectDenied:
		if ok || r.Domain != "win32" || r.Code != accessDenied {
			return "expected denied"
		}
	case ExpectRefused:
		if ok || c.Diagnostic == "" || !strings.Contains(r.Diagnostic, c.Diagnostic) {
			return "expected refused with the product's diagnostic"
		}
	}
	return ""
}
