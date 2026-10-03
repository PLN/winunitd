package pathcases

import (
	"strings"
	"testing"
)

// passing builds an observation that meets every rule of a case: the
// case's genuine actor, unchanged and restored objects, a reparse link and
// each step's expected result.
func passing(c Case) *Observation {
	o := &Observation{Schema: ObservationSchema, Case: c.ID, Ledger: Hash(), Baseline: map[string]ObjectState{}, Before: map[string]ObjectState{},
		After: map[string]ObjectState{}, Restored: map[string]ObjectState{}}
	switch c.Actor {
	case ActorStandard, ActorUser:
		o.Actor = &ActorFacts{SID: "S-1-5-21-1-2-3-1001", Session: 2, ElevationType: elevationTypeDefault, Integrity: "medium"}
	case ActorFilteredAdmin:
		o.Actor = &ActorFacts{SID: "S-1-5-21-1-2-3-1002", Session: 3, ElevationType: elevationTypeLimited, AdminFlags: groupUseForDenyOnly, Integrity: "medium"}
	}
	for _, role := range c.Roles {
		base := ObjectState{Exists: true, FileID: "vol-1:" + role, Owner: "S-1-5-32-544", DACL: "D:P(A;;FA;;;SY)(A;;FA;;;BA)", SHA256: strings.Repeat("a", 64)}
		shaped := base
		if role == RoleLink {
			// The fixture made the link; restoration removed it again.
			base = ObjectState{}
			shaped = ObjectState{Exists: true, FileID: "vol-1:link", ReparseTag: 0xa0000003, LinkTarget: strings.Repeat("b", 64), Owner: "S-1-5-18"}
		}
		o.Baseline[role], o.Before[role], o.After[role], o.Restored[role] = base, shaped, shaped, base
	}
	for _, s := range c.Steps {
		r := StepResult{Name: s.Name}
		switch s.Expect {
		case ExpectOK, ExpectNoFollow:
			r.OK = true
		case ExpectDenied:
			r.Win32 = errorAccessDenied
		case ExpectRefused:
			r.Win32, r.Marker = 1603, true
		}
		o.Steps = append(o.Steps, r)
	}
	return o
}

func TestEvaluateDecidedCasesPass(t *testing.T) {
	l := testLedger(t)
	for _, c := range l.Cases {
		v := Evaluate(l, passing(c))
		if c.Outcome == OutcomeDecisionNeeded {
			if v.Pass || !strings.Contains(strings.Join(v.Problems, "\n"), "awaits a product policy decision") {
				t.Errorf("%s: an undecided case passed: %v", c.ID, v.Problems)
			}
			continue
		}
		if !v.Pass {
			t.Errorf("%s: %v", c.ID, v.Problems)
		}
	}
}

func TestEvaluateRefusesContradictions(t *testing.T) {
	l := testLedger(t)
	step := func(o *Observation, name string) *StepResult {
		for i := range o.Steps {
			if o.Steps[i].Name == name {
				return &o.Steps[i]
			}
		}
		t.Fatalf("no step %s", name)
		return nil
	}
	for _, tc := range []struct {
		name, id, want string
		mutate         func(*Observation)
	}{
		{"SYSTEM as the standard caller", "data-child-rename-standard", "not a genuine non-elevated standard token", func(o *Observation) { o.Actor.SID = systemSID }},
		{"elevated caller", "data-child-rename-filtered", "not a genuine non-elevated filtered-admin token", func(o *Observation) { o.Actor.Elevated = true }},
		{"no actor token", "user-units-nested-junction", "not a genuine non-elevated user token", func(o *Observation) { o.Actor = nil }},
		{"standard token for the filtered lane", "data-child-rename-filtered", "not a genuine UAC-filtered administrator", func(o *Observation) {
			o.Actor.ElevationType, o.Actor.AdminFlags = elevationTypeDefault, 0
		}},
		{"filtered token with enabled Administrators", "data-child-rename-filtered", "not a genuine UAC-filtered administrator", func(o *Observation) {
			o.Actor.AdminFlags |= groupEnabled
		}},
		{"high-integrity standard caller", "data-child-rename-standard", "not a standard interactive caller", func(o *Observation) { o.Actor.Integrity = "high" }},
		{"session-zero caller", "data-child-rename-standard", "not a standard interactive caller", func(o *Observation) { o.Actor.Session = 0 }},
		{"target changed", "user-units-nested-junction", "the target changed", func(o *Observation) {
			s := o.After[RoleTarget]
			s.SHA256 = strings.Repeat("c", 64)
			o.After[RoleTarget] = s
		}},
		{"protected leaf replaced", "data-child-rename-standard", "the leaf changed", func(o *Observation) {
			s := o.After[RoleLeaf]
			s.FileID = "vol-1:other"
			o.After[RoleLeaf] = s
		}},
		{"link retargeted", "user-winunitd-component-junction", "the link changed", func(o *Observation) {
			s := o.After[RoleLink]
			s.LinkTarget = strings.Repeat("d", 64)
			o.After[RoleLink] = s
		}},
		{"link was a plain directory", "user-units-file-symlink", "was not a reparse point", func(o *Observation) {
			for _, m := range []map[string]ObjectState{o.Before, o.After} {
				s := m[RoleLink]
				s.ReparseTag = 0
				m[RoleLink] = s
			}
		}},
		{"sibling gone", "user-units-nested-junction", "the sibling is missing", func(o *Observation) { o.After[RoleSibling] = ObjectState{} }},
		{"link left behind", "user-units-file-symlink", "the link was not restored", func(o *Observation) { o.Restored[RoleLink] = o.After[RoleLink] }},
		{"target ACL not restored", "data-child-rename-filtered", "the target was not restored", func(o *Observation) {
			s := o.Restored[RoleTarget]
			s.DACL += "(A;;FA;;;BU)"
			o.Restored[RoleTarget] = s
		}},
		{"no restoration record", "user-winunitd-component-junction", "lacks the sibling", func(o *Observation) { delete(o.Restored, RoleSibling) }},
		{"step not run", "data-child-rename-standard", "step rename-own-directory was not run", func(o *Observation) { o.Steps = o.Steps[:1] }},
		{"step outside the case", "data-child-rename-standard", "step chmod is not in the case", func(o *Observation) {
			o.Steps = append(o.Steps, StepResult{Name: "chmod", OK: true})
		}},
		{"step recorded twice", "data-child-rename-standard", "recorded twice", func(o *Observation) { o.Steps = append(o.Steps, o.Steps[0]) }},
		{"denial with another error", "data-child-rename-standard", "step rename-units: expected denied", func(o *Observation) {
			step(o, "rename-units").Win32 = 32
		}},
		{"rename succeeded", "data-child-rename-filtered", "step rename-units: expected denied", func(o *Observation) {
			s := step(o, "rename-units")
			s.OK, s.Win32 = true, 0
		}},
		{"refusal without the diagnostic", "user-winunitd-component-junction", "step delegated-admission: expected refused", func(o *Observation) {
			step(o, "delegated-admission").Marker = false
		}},
		{"consumer followed the link", "user-units-nested-junction", "step delegated-admission: expected no-follow", func(o *Observation) {
			step(o, "delegated-admission").Canary = true
		}},
		{"consumer failed instead of ignoring the link", "user-units-file-symlink", "step delegated-admission: expected no-follow", func(o *Observation) {
			step(o, "delegated-admission").OK = false
		}},
		{"control broken", "user-units-nested-junction", "step peer-regular-unit-admitted: expected ok", func(o *Observation) {
			step(o, "peer-regular-unit-admitted").OK = false
		}},
		{"another ledger", "user-units-nested-junction", "names another ledger", func(o *Observation) { o.Ledger = strings.Repeat("0", 64) }},
		{"schema", "user-units-nested-junction", "observation schema", func(o *Observation) { o.Schema = 2 }},
	} {
		c, ok := l.Case(tc.id)
		if !ok {
			t.Fatalf("%s: no case %s", tc.name, tc.id)
		}
		o := passing(c)
		tc.mutate(o)
		v := Evaluate(l, o)
		if v.Pass || !strings.Contains(strings.Join(v.Problems, "\n"), tc.want) {
			t.Errorf("%s: problems %q", tc.name, v.Problems)
		}
	}
	if v := Evaluate(l, &Observation{Schema: ObservationSchema, Case: "no-such-case", Ledger: Hash()}); v.Pass {
		t.Error("an unknown case passed")
	}
}
