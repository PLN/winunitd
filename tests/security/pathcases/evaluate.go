package pathcases

import (
	"fmt"
	"slices"
)

// ObservationSchema is the observation format version.
const ObservationSchema = 1

// systemSID is LocalSystem.
const systemSID = "S-1-5-18"

// Token elevation types and group attributes the actor checks use.
const (
	elevationTypeDefault = 1
	elevationTypeLimited = 3
	groupEnabled         = 0x4
	groupUseForDenyOnly  = 0x10
)

// Observation is one native execution of a case, as the fixture's SYSTEM
// observer and the actor's own probe recorded it.
type Observation struct {
	Schema int    `json:"schema"`
	Case   string `json:"case"`
	// Ledger is the hash of the ledger the case was taken from.
	Ledger string `json:"ledger"`
	// Actor is the actor's own token, read by the actor's process; absent
	// for a fixture-created state.
	Actor *ActorFacts `json:"actor,omitempty"`
	// Object states per role: before the fixture made the shape, after it
	// and before the steps, after the steps, and after restoration.
	Baseline map[string]ObjectState `json:"baseline"`
	Before   map[string]ObjectState `json:"before"`
	After    map[string]ObjectState `json:"after"`
	Restored map[string]ObjectState `json:"restored"`
	Steps    []StepResult           `json:"steps"`
}

// ActorFacts is the actor's token.
type ActorFacts struct {
	SID           string `json:"sid"`
	Session       uint32 `json:"session"`
	Elevated      bool   `json:"elevated"`
	ElevationType uint32 `json:"elevationType"`
	// AdminFlags are the Administrators group's attributes in the token,
	// zero when the token has no such group.
	AdminFlags uint32 `json:"adminFlags"`
	Integrity  string `json:"integrity"`
}

// ObjectState is a path's identity, read without following a reparse
// point: whether it exists, its file ID, reparse tag and a hash of the
// link's substitute name, its owner and DACL, and a file's content hash.
type ObjectState struct {
	Exists     bool   `json:"exists"`
	FileID     string `json:"fileId,omitempty"`
	ReparseTag uint32 `json:"reparseTag,omitempty"`
	LinkTarget string `json:"linkTarget,omitempty"`
	Owner      string `json:"owner,omitempty"`
	DACL       string `json:"dacl,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
}

// StepResult is one step's observed result: whether it succeeded, its
// Win32 code, whether the product's expected diagnostic was seen, and
// whether the consumer's result shows the canary the target holds.
type StepResult struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Win32  uint32 `json:"win32,omitempty"`
	Marker bool   `json:"marker,omitempty"`
	Canary bool   `json:"canary,omitempty"`
}

const errorAccessDenied = 5

// Verdict is a case's recomputed result.
type Verdict struct {
	Case     string   `json:"case"`
	Pass     bool     `json:"pass"`
	Problems []string `json:"problems,omitempty"`
}

// Evaluate recomputes a case's verdict from one observation. A case
// awaiting a policy decision never passes, whatever was observed.
func Evaluate(l *Ledger, o *Observation) Verdict {
	v := Verdict{Case: o.Case}
	add := func(format string, args ...any) { v.Problems = append(v.Problems, fmt.Sprintf(format, args...)) }
	c, ok := l.Case(o.Case)
	switch {
	case o.Schema != ObservationSchema:
		add("observation schema %d", o.Schema)
	case !ok:
		add("no such case")
	case o.Ledger != Hash():
		add("the observation names another ledger")
	}
	if !ok {
		return v
	}
	if c.Outcome == OutcomeDecisionNeeded {
		add("the case awaits a product policy decision")
	}
	if p := actorProblem(c.Actor, o.Actor); p != "" {
		add("%s", p)
	}
	for _, role := range c.Roles {
		states := []map[string]ObjectState{o.Baseline, o.Before, o.After, o.Restored}
		if slices.ContainsFunc(states, func(m map[string]ObjectState) bool { _, has := m[role]; return !has }) {
			add("the observation lacks the %s at some stage", role)
			continue
		}
		if o.Restored[role] != o.Baseline[role] {
			add("the %s was not restored to its baseline", role)
		}
	}
	// The external target, a protected leaf and the link itself are the
	// same objects after the steps; the sibling still exists.
	for _, role := range []string{RoleTarget, RoleLeaf, RoleLink} {
		if !slices.Contains(c.Roles, role) {
			continue
		}
		before, after := o.Before[role], o.After[role]
		if !before.Exists || before != after {
			add("the %s changed or is missing", role)
		}
	}
	if slices.Contains(c.Roles, RoleLink) && o.Before[RoleLink].ReparseTag == 0 {
		add("the link was not a reparse point when the steps ran")
	}
	if !o.After[RoleSibling].Exists {
		add("the sibling is missing after the steps")
	}
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
		if !meets(s.Expect, r) {
			add("step %s: expected %s", s.Name, s.Expect)
		}
	}
	for name := range results {
		add("step %s is not in the case", name)
	}
	v.Pass = len(v.Problems) == 0
	return v
}

// meets reports whether a step result is the expected kind: success; an
// access denial; a refusal with the product's diagnostic; or success that
// never showed the target's canary.
func meets(expect string, r StepResult) bool {
	switch expect {
	case ExpectOK:
		return r.OK && !r.Canary
	case ExpectDenied:
		return !r.OK && r.Win32 == errorAccessDenied
	case ExpectRefused:
		return !r.OK && r.Marker && !r.Canary
	case ExpectNoFollow:
		return r.OK && !r.Canary
	}
	return false
}

// actorProblem checks the actor's own token against the case's actor: a
// SYSTEM or elevated substitute never stands in for a standard or filtered
// caller or the account itself.
func actorProblem(actor string, a *ActorFacts) string {
	if actor == ActorSystemFixture {
		return ""
	}
	if a == nil || a.SID == "" || a.SID == systemSID || a.Elevated {
		return "the actor is not a genuine non-elevated " + actor + " token"
	}
	switch actor {
	case ActorStandard:
		if a.Session == 0 || a.ElevationType != elevationTypeDefault || a.AdminFlags != 0 || a.Integrity != "medium" {
			return "the actor is not a standard interactive caller"
		}
	case ActorFilteredAdmin:
		if a.Session == 0 || a.ElevationType != elevationTypeLimited || a.AdminFlags&groupUseForDenyOnly == 0 || a.AdminFlags&groupEnabled != 0 ||
			a.Integrity != "medium" {
			return "the actor is not a genuine UAC-filtered administrator"
		}
	case ActorUser:
		if a.AdminFlags&groupEnabled != 0 {
			return "the account's own context holds enabled Administrators"
		}
	}
	return ""
}
