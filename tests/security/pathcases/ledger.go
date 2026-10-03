// Package pathcases is the reviewed ledger of nested-descendant and
// ancestor-path security cases that remain open in R4, and the evaluator
// that recomputes each case's verdict from raw native observations. It is
// qualification code; no release payload uses it.
//
// Every case names the privileged code path that touches the path, the
// actor, the shape, a safe sibling control and the external target that
// must not change. Its expected outcome comes from a documented product
// rule; a case on which the product is silent is decision-needed and can
// never pass, so no case invents a path restriction.
package pathcases

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
)

//go:embed ledger.json
var ledgerJSON []byte

// LedgerSchema is the ledger format version.
const LedgerSchema = 1

// Outcomes a case can expect.
const (
	// OutcomeProtected: the actor's attempts are denied by the documented
	// ACL.
	OutcomeProtected = "protected"
	// OutcomeRefuse: the consumer refuses with its exact diagnostic.
	OutcomeRefuse = "refuse"
	// OutcomeNoFollow: the consumer works and never reaches the target.
	OutcomeNoFollow = "no-follow"
	// OutcomeDecisionNeeded: the product is silent; the case cannot pass.
	OutcomeDecisionNeeded = "decision-needed"
)

// Step expectations.
const (
	ExpectOK       = "ok"
	ExpectDenied   = "denied"
	ExpectRefused  = "refused"
	ExpectNoFollow = "no-follow"
)

// Actors.
const (
	ActorStandard      = "standard"
	ActorFilteredAdmin = "filtered-admin"
	ActorUser          = "user"
	ActorSystemFixture = "system-fixture"
)

// Observed object roles: the link the fixture made; the target, the
// object an attack would reach (a link's external target, or the protected
// content behind a leaf); the protected leaf an ancestor case attempts to
// rename; the leaf's parent, through whose rights it would be renamed; and
// a safe sibling that must keep working.
const (
	RoleLink     = "link"
	RoleTarget   = "target"
	RoleLeaf     = "leaf"
	RoleAncestor = "ancestor"
	RoleSibling  = "sibling"
)

// Step contexts: who performs a step. The actor itself; a SYSTEM consumer;
// a SYSTEM consumer impersonating the case's account, as the delegated
// admission probe does; or one impersonating the peer account.
const (
	ContextActor            = "actor"
	ContextSystem           = "system"
	ContextUserImpersonated = "user-impersonated"
	ContextPeerImpersonated = "peer-impersonated"
)

// Witnesses: the consumer's own result a step must show.
var (
	positiveWitnesses = []string{"admitted", "loaded", "written", "granted"}
	negativeWitnesses = []string{"not-admitted", "not-loaded", "not-written", "not-granted"}
)

var (
	trees = []string{"data-root", "units", "enabled", "journal", "runtime", "linger", "daemon", "install", "user-root", "user-units", "programdata",
		"programfiles"}
	shapes     = []string{"descendant-junction", "descendant-file-link", "component-junction", "ancestor-rename"}
	actors     = []string{ActorStandard, ActorFilteredAdmin, ActorUser, ActorSystemFixture}
	outcomes   = []string{OutcomeProtected, OutcomeRefuse, OutcomeNoFollow, OutcomeDecisionNeeded}
	expects    = []string{ExpectOK, ExpectDenied, ExpectRefused, ExpectNoFollow}
	roles      = []string{RoleLink, RoleTarget, RoleLeaf, RoleAncestor, RoleSibling}
	contexts   = []string{ContextActor, ContextSystem, ContextUserImpersonated, ContextPeerImpersonated}
	caseID     = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	stepName   = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	policyLink = regexp.MustCompile(`^docs/[A-Za-z0-9._-]+\.md#[a-z0-9-]+$`)
)

// Ledger is the case ledger.
type Ledger struct {
	Schema    int               `json:"schema"`
	Consumers map[string]string `json:"consumers"`
	Policies  map[string]Policy `json:"policies"`
	// Qualified lists the shapes earlier qualifications already cover;
	// the ledger does not repeat them. Deferred lists residual shapes this
	// ledger does not cover yet.
	Qualified []string `json:"qualified"`
	Deferred  []string `json:"deferred"`
	Cases     []Case   `json:"cases"`
}

// Policy is a documented product rule a case rests on.
type Policy struct {
	Source    string `json:"source"`
	Statement string `json:"statement"`
}

// Case is one path case.
type Case struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Tree  string `json:"tree"`
	Shape string `json:"shape"`
	Depth int    `json:"depth"`
	Path  string `json:"path"`
	Actor string `json:"actor"`
	// Mode is the actor's logon: wts for an interactive caller, or wts or
	// s4u for the account's own context; empty for a fixture-made state.
	Mode     string `json:"mode,omitempty"`
	Consumer string `json:"consumer"`
	// Ancestor names the leaf's parent an ancestor case acts through.
	Ancestor   string `json:"ancestor,omitempty"`
	Outcome    string `json:"outcome"`
	Policy     string `json:"policy,omitempty"`
	Diagnostic string `json:"diagnostic,omitempty"`
	Question   string `json:"question,omitempty"`
	// Recommendation is the reviewer's recommended answer to the question,
	// for the maintainer; it decides nothing.
	Recommendation string   `json:"recommendation,omitempty"`
	Basis          string   `json:"basis"`
	Roles          []string `json:"roles"`
	// RoleKinds is the object kind, file or directory, every role but the
	// link must be; the link's kind follows from the shape.
	RoleKinds map[string]string `json:"roleKinds"`
	Steps     []Step            `json:"steps"`
}

// Step is one operation of a case: by the actor, by the consumer, or a
// control that must keep working; who performs it; what it must show; and
// the consumer's own result it must carry.
type Step struct {
	Name    string `json:"name"`
	By      string `json:"by"`
	Context string `json:"context"`
	Expect  string `json:"expect"`
	Witness string `json:"witness,omitempty"`
}

// Load decodes and validates the embedded ledger.
func Load() (*Ledger, error) { return Decode(ledgerJSON) }

// Digest is the SHA-256 of a ledger's canonical encoding: the policy an
// observation is evaluated against, whatever bytes it was read from.
func (l *Ledger) Digest() string {
	data, err := json.Marshal(l)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Decode reads a ledger strictly, refusing unknown fields, duplicate keys
// and anything after it, and validates it.
func Decode(data []byte) (*Ledger, error) {
	if err := noDuplicateKeys(data); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var l Ledger
	if err := dec.Decode(&l); err != nil {
		return nil, err
	}
	var rest json.RawMessage
	if err := dec.Decode(&rest); err != io.EOF {
		return nil, errors.New("trailing data after the ledger")
	}
	return &l, l.Validate()
}

// noDuplicateKeys refuses a JSON document with a repeated object key, which
// a decoder would otherwise resolve silently to the last value.
func noDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	type frame struct {
		object bool
		keys   map[string]bool
		key    bool
	}
	var stack []*frame
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		var top *frame
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{', '[':
				if top != nil && top.object {
					top.key = true
				}
				stack = append(stack, &frame{object: t == '{', keys: map[string]bool{}, key: true})
			default:
				stack = stack[:len(stack)-1]
			}
			continue
		case string:
			if top != nil && top.object && top.key {
				if top.keys[t] {
					return fmt.Errorf("duplicate key %q", t)
				}
				top.keys[t] = true
				top.key = false
				continue
			}
		}
		if top != nil && top.object {
			top.key = true
		}
	}
}

// Case returns the case with an ID.
func (l *Ledger) Case(id string) (Case, bool) {
	i := slices.IndexFunc(l.Cases, func(c Case) bool { return c.ID == id })
	if i < 0 {
		return Case{}, false
	}
	return l.Cases[i], true
}

// Validate checks the ledger's shape and every case's internal rules.
func (l *Ledger) Validate() error {
	if l.Schema != LedgerSchema {
		return fmt.Errorf("ledger schema %d", l.Schema)
	}
	if len(l.Consumers) == 0 || len(l.Cases) == 0 || len(l.Qualified) == 0 || len(l.Deferred) == 0 {
		return errors.New("the ledger needs consumers, cases, the qualified shapes it does not repeat and the shapes it defers")
	}
	for name, p := range l.Policies {
		if !policyLink.MatchString(p.Source) || p.Statement == "" {
			return fmt.Errorf("policy %s needs a docs source with an anchor and a statement", name)
		}
	}
	seen := map[string]bool{}
	for _, c := range l.Cases {
		if !caseID.MatchString(c.ID) || seen[c.ID] {
			return fmt.Errorf("case ID %q", c.ID)
		}
		seen[c.ID] = true
		if err := l.validateCase(c); err != nil {
			return fmt.Errorf("case %s: %w", c.ID, err)
		}
	}
	// Ancestor cases test both genuine interactive actors.
	for _, c := range l.Cases {
		twin := map[string]string{ActorStandard: ActorFilteredAdmin, ActorFilteredAdmin: ActorStandard}[c.Actor]
		if twin != "" && !slices.ContainsFunc(l.Cases, func(o Case) bool {
			return o.Actor == twin && o.Tree == c.Tree && o.Shape == c.Shape && o.Path == c.Path && o.Outcome == c.Outcome
		}) {
			return fmt.Errorf("case %s has no %s twin", c.ID, twin)
		}
	}
	return nil
}

func (l *Ledger) validateCase(c Case) error {
	switch {
	case c.Title == "" || c.Path == "" || c.Basis == "":
		return errors.New("needs a title, a path and its basis")
	case !slices.Contains(trees, c.Tree):
		return fmt.Errorf("tree %q", c.Tree)
	case !slices.Contains(shapes, c.Shape):
		return fmt.Errorf("shape %q", c.Shape)
	case !slices.Contains(actors, c.Actor):
		return fmt.Errorf("actor %q", c.Actor)
	case l.Consumers[c.Consumer] == "":
		return fmt.Errorf("consumer %q", c.Consumer)
	case !slices.Contains(outcomes, c.Outcome):
		return fmt.Errorf("outcome %q", c.Outcome)
	}
	// A descendant sits below an immediate child of its tree; the immediate
	// children are the qualified shapes.
	if (c.Shape == "descendant-junction" || c.Shape == "descendant-file-link") && c.Depth < 2 {
		return errors.New("a descendant shape is at least two levels deep")
	}
	if c.Depth < 1 {
		return errors.New("depth")
	}
	if c.Policy != "" {
		if _, ok := l.Policies[c.Policy]; !ok {
			return fmt.Errorf("policy %q", c.Policy)
		}
	}
	for _, r := range c.Roles {
		if !slices.Contains(roles, r) {
			return fmt.Errorf("role %q", r)
		}
	}
	if !slices.Contains(c.Roles, RoleTarget) || !slices.Contains(c.Roles, RoleSibling) {
		return errors.New("needs the external target and a safe sibling among its roles")
	}
	// Every role but the link has its object kind, bound to the shape: an
	// ancestor case renames a directory through its directory parent; a
	// file link reaches a file, a junction a directory.
	for _, r := range c.Roles {
		k, ok := c.RoleKinds[r]
		switch {
		case r == RoleLink && ok:
			return errors.New("the link's kind follows from the shape")
		case r != RoleLink && k != "file" && k != "directory":
			return fmt.Errorf("role %s needs its object kind", r)
		}
	}
	if len(c.RoleKinds) != len(c.Roles)-boolInt(slices.Contains(c.Roles, RoleLink)) {
		return errors.New("an object kind for a role the case does not observe")
	}
	switch c.Shape {
	case "ancestor-rename":
		if c.RoleKinds[RoleAncestor] != "directory" || c.RoleKinds[RoleLeaf] != "directory" {
			return errors.New("an ancestor case renames a directory through its directory parent")
		}
	case "descendant-file-link":
		if c.RoleKinds[RoleTarget] != "file" {
			return errors.New("a file link reaches a file")
		}
	case "descendant-junction", "component-junction":
		if c.RoleKinds[RoleTarget] != "directory" {
			return errors.New("a junction reaches a directory")
		}
	}
	linked := c.Shape == "descendant-junction" || c.Shape == "descendant-file-link" || c.Shape == "component-junction"
	if linked != slices.Contains(c.Roles, RoleLink) {
		return errors.New("a link shape, and only one, observes its link")
	}
	ancestral := c.Shape == "ancestor-rename"
	if ancestral != (slices.Contains(c.Roles, RoleAncestor) && slices.Contains(c.Roles, RoleLeaf) && c.Ancestor != "") {
		return errors.New("an ancestor case, and only one, names and observes the leaf's parent")
	}
	// The actor's logon: an interactive caller is a WTS logon; the
	// account's own context is WTS or the product's S4U; a fixture-made
	// state has none.
	switch c.Actor {
	case ActorStandard, ActorFilteredAdmin:
		if c.Mode != "wts" {
			return errors.New("an interactive caller is a wts logon")
		}
	case ActorUser:
		if c.Mode != "wts" && c.Mode != "s4u" {
			return errors.New("the account's own context is wts or s4u")
		}
	default:
		if c.Mode != "" {
			return errors.New("a fixture-made state has no logon mode")
		}
	}
	names := map[string]bool{}
	count := map[string]map[string]int{}
	for _, s := range c.Steps {
		if !stepName.MatchString(s.Name) || names[s.Name] {
			return fmt.Errorf("step name %q", s.Name)
		}
		names[s.Name] = true
		if !slices.Contains(expects, s.Expect) {
			return fmt.Errorf("step %s expects %q", s.Name, s.Expect)
		}
		if !slices.Contains(contexts, s.Context) {
			return fmt.Errorf("step %s context %q", s.Name, s.Context)
		}
		if s.Witness != "" && !slices.Contains(positiveWitnesses, s.Witness) && !slices.Contains(negativeWitnesses, s.Witness) {
			return fmt.Errorf("step %s witness %q", s.Name, s.Witness)
		}
		if err := l.validateStep(c, s); err != nil {
			return fmt.Errorf("step %s: %w", s.Name, err)
		}
		if count[s.By] == nil {
			count[s.By] = map[string]int{}
		}
		count[s.By][s.Expect]++
	}
	if count["control"][ExpectOK] == 0 || len(count["control"]) != 1 {
		return errors.New("needs a safe sibling control that must keep working, and only such controls")
	}
	if count["actor"] != nil && c.Actor == ActorSystemFixture {
		return errors.New("a fixture-created state has no actor steps")
	}
	// The outcome is the documented rule the steps carry out.
	switch c.Outcome {
	case OutcomeDecisionNeeded:
		if c.Question == "" {
			return errors.New("a decision-needed case states the policy question")
		}
	case OutcomeProtected:
		if c.Policy == "" || count["actor"][ExpectDenied] == 0 || c.Actor != ActorStandard && c.Actor != ActorFilteredAdmin {
			return errors.New("a protected case rests on a policy and a genuine caller's denied attempt")
		}
	case OutcomeRefuse:
		if c.Policy == "" || c.Diagnostic == "" || count["consumer"][ExpectRefused] == 0 || len(count["consumer"]) != 1 {
			return errors.New("a refuse case rests on a policy and every consumer step refuses with its diagnostic")
		}
	case OutcomeNoFollow:
		if c.Policy == "" || count["consumer"][ExpectNoFollow] == 0 || len(count["consumer"]) != 1 {
			return errors.New("a no-follow case rests on a policy and every consumer step avoids the target")
		}
	}
	if c.Outcome != OutcomeDecisionNeeded && c.Question != "" {
		return errors.New("a decided case has no open question")
	}
	if c.Outcome != OutcomeRefuse && c.Diagnostic != "" {
		return errors.New("only a refuse case names a diagnostic")
	}
	if c.Recommendation != "" && c.Outcome != OutcomeDecisionNeeded {
		return errors.New("only an open question carries a recommendation")
	}
	return nil
}

// validateStep ties a step's performer and witness to its kind: the actor
// acts in its own context; the os-acl consumer is the caller's own
// operation; the delegated admission probe impersonates the account; other
// consumers run as SYSTEM. A consumer step that must avoid the target, and
// a consumer control that must keep working, show the consumer's own
// result.
func (l *Ledger) validateStep(c Case, s Step) error {
	switch s.By {
	case "actor":
		if s.Context != ContextActor {
			return errors.New("an actor step runs in the actor's context")
		}
	case "consumer":
		want := ContextSystem
		switch c.Consumer {
		case "os-acl":
			return errors.New("the caller's own operation is an actor step")
		case "admission-probe":
			want = ContextUserImpersonated
		}
		if s.Context != want {
			return fmt.Errorf("a %s step runs in context %s", c.Consumer, want)
		}
		if s.Expect == ExpectNoFollow && !slices.Contains(negativeWitnesses, s.Witness) {
			return errors.New("a no-follow step shows the consumer's own negative result")
		}
		if s.Expect != ExpectNoFollow && s.Witness != "" {
			return errors.New("only a no-follow consumer step carries a witness")
		}
	case "control":
		switch s.Context {
		case ContextActor:
			if c.Actor == ActorSystemFixture || s.Witness != "" {
				return errors.New("an actor control needs an actor and carries no witness")
			}
		case ContextSystem, ContextPeerImpersonated:
			if s.Context == ContextPeerImpersonated && c.Consumer != "admission-probe" {
				return errors.New("only the admission probe impersonates the peer")
			}
			if !slices.Contains(positiveWitnesses, s.Witness) {
				return errors.New("a consumer control shows the consumer's own positive result")
			}
		default:
			return errors.New("a control runs as the actor, SYSTEM or the impersonated peer")
		}
	default:
		return fmt.Errorf("by %q", s.By)
	}
	if s.Context == ContextActor && c.Actor == ActorSystemFixture {
		return errors.New("a fixture-made state has no actor context")
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
