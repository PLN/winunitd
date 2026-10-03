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
// rename; and a safe sibling that must keep working.
const (
	RoleLink    = "link"
	RoleTarget  = "target"
	RoleLeaf    = "leaf"
	RoleSibling = "sibling"
)

var (
	trees      = []string{"data-root", "units", "enabled", "journal", "runtime", "linger", "daemon", "install", "user-root", "user-units", "programdata"}
	shapes     = []string{"descendant-junction", "descendant-file-link", "component-junction", "ancestor-rename"}
	actors     = []string{ActorStandard, ActorFilteredAdmin, ActorUser, ActorSystemFixture}
	outcomes   = []string{OutcomeProtected, OutcomeRefuse, OutcomeNoFollow, OutcomeDecisionNeeded}
	expects    = []string{ExpectOK, ExpectDenied, ExpectRefused, ExpectNoFollow}
	roles      = []string{RoleLink, RoleTarget, RoleLeaf, RoleSibling}
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
	// the ledger does not repeat them.
	Qualified []string `json:"qualified"`
	Cases     []Case   `json:"cases"`
}

// Policy is a documented product rule a case rests on.
type Policy struct {
	Source    string `json:"source"`
	Statement string `json:"statement"`
}

// Case is one path case.
type Case struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Tree       string   `json:"tree"`
	Shape      string   `json:"shape"`
	Depth      int      `json:"depth"`
	Path       string   `json:"path"`
	Actor      string   `json:"actor"`
	Consumer   string   `json:"consumer"`
	Outcome    string   `json:"outcome"`
	Policy     string   `json:"policy,omitempty"`
	Diagnostic string   `json:"diagnostic,omitempty"`
	Question   string   `json:"question,omitempty"`
	Basis      string   `json:"basis"`
	Roles      []string `json:"roles"`
	Steps      []Step   `json:"steps"`
}

// Step is one operation of a case: by the actor, by the consumer, or a
// control that must keep working.
type Step struct {
	Name   string `json:"name"`
	By     string `json:"by"`
	Expect string `json:"expect"`
}

// Load decodes and validates the embedded ledger.
func Load() (*Ledger, error) { return Decode(ledgerJSON) }

// Hash is the SHA-256 of the embedded ledger's bytes, which observations
// name.
func Hash() string {
	sum := sha256.Sum256(ledgerJSON)
	return hex.EncodeToString(sum[:])
}

// Decode reads a ledger strictly and validates it.
func Decode(data []byte) (*Ledger, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var l Ledger
	if err := dec.Decode(&l); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("trailing data after the ledger")
	}
	return &l, l.Validate()
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
	if len(l.Consumers) == 0 || len(l.Cases) == 0 || len(l.Qualified) == 0 {
		return errors.New("the ledger needs consumers, cases and the qualified shapes it does not repeat")
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
		switch s.By {
		case "actor", "consumer", "control":
		default:
			return fmt.Errorf("step %s by %q", s.Name, s.By)
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
	return nil
}
