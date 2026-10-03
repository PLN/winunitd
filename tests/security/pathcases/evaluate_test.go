package pathcases

import (
	"encoding/json"
	"strings"
	"testing"
)

const (
	sidStandard = "S-1-5-21-1-2-3-1001"
	sidFiltered = "S-1-5-21-1-2-3-1003"
	sidPeer     = "S-1-5-21-1-2-3-1002"
	localServ   = "S-1-5-19"
)

var testRun = Run{Source: strings.Repeat("a", 40), Admission: strings.Repeat("b", 64),
	Accounts: map[string]string{ActorStandard: sidStandard, ActorFilteredAdmin: sidFiltered, ActorUser: sidStandard, "peer": sidPeer}}

var stageAt = map[string]uint64{stageBaseline: 100, stageBefore: 200, stageStepsBegin: 300, stageStepsEnd: 400, stageAfter: 500, stageRestored: 600}

func rights(v uint32) *uint32 { return &v }
func exposed(v bool) *bool    { return &v }

// actorFor is the genuine token of a case's actor in the test run.
func actorFor(c Case) *Process {
	switch {
	case c.Actor == ActorStandard:
		return &Process{PID: 700, Created: 650, Token: TokenFacts{SID: sidStandard, Session: 2, ElevationType: elevationTypeDefault, Integrity: "medium",
			Source: "User32", LogonType: 2, AuthenticationID: "00000000:0001a000"}}
	case c.Actor == ActorFilteredAdmin:
		return &Process{PID: 701, Created: 651, Token: TokenFacts{SID: sidFiltered, Session: 3, ElevationType: elevationTypeLimited, AdminFlags: groupUseForDenyOnly,
			Integrity: "medium", Source: "User32", LogonType: 10, AuthenticationID: "00000000:0001b000"}}
	case c.Actor == ActorUser && c.Mode == "s4u":
		return &Process{PID: 702, Created: 652, Token: TokenFacts{SID: sidStandard, Source: productTokenSource, LogonType: 3, Integrity: "medium",
			AuthenticationID: "00000000:0001c000"}}
	case c.Actor == ActorUser:
		return &Process{PID: 703, Created: 653, Token: TokenFacts{SID: sidStandard, Session: 2, ElevationType: elevationTypeDefault, Integrity: "medium",
			Source: "User32", LogonType: 2, AuthenticationID: "00000000:0001d000"}}
	}
	return nil
}

// passing builds an observation of a case that meets every rule: the
// admitted run's envelope, the genuine actor, every object read with its
// facts, a link of the shape's kind resolving to the target, unchanged and
// restored objects, and each step performed in its context with a coherent
// result of the expected kind.
func passing(l *Ledger, c Case) *Observation {
	o := &Observation{Schema: ObservationSchema, Ledger: l.Digest(), Case: c.ID, Actor: actorFor(c), States: map[string]map[string]ObjectState{},
		Envelope: Envelope{Source: testRun.Source, Admission: testRun.Admission, Run: "run-1", Execution: "x-" + c.ID,
			Observer: Process{PID: 500, Created: 50, Token: TokenFacts{SID: systemSID}}}}
	for _, name := range stageOrder {
		o.Envelope.Stages = append(o.Envelope.Stages, StageMark{Name: name, At: stageAt[name]})
	}
	for _, stage := range stateStages {
		o.States[stage] = map[string]ObjectState{}
		for _, role := range c.Roles {
			s := ObjectState{At: stageAt[stage] + 10, Read: true, Exists: true, Kind: c.RoleKinds[role], FileID: "vol-1:" + role, Owner: "S-1-5-32-544",
				DACL: "D:P(A;;FA;;;SY)(A;;FA;;;BA)", Content: strings.Repeat("c", 64)}
			if role == RoleLink {
				if stage == stageBaseline || stage == stageRestored {
					s = ObjectState{At: stageAt[stage] + 10, Read: true}
				} else {
					s = ObjectState{At: stageAt[stage] + 10, Read: true, Exists: true, Kind: "junction", ReparseTag: tagMountPoint, FileID: "vol-1:link",
						Owner: sidStandard, DACL: "D:(A;;FA;;;BU)", ResolvedID: "vol-1:" + RoleTarget}
					if c.Shape == "descendant-file-link" {
						s.Kind, s.ReparseTag = "symlink-file", tagSymlink
					}
				}
			}
			if stage == stageBefore && (role == RoleAncestor || role == RoleLeaf) {
				s.ActorRights = rights(0x1 | 0x4)
			}
			o.States[stage][role] = s
		}
	}
	for _, st := range c.Steps {
		r := StepResult{Name: st.Name, At: 350, Outcome: "succeeded", TargetExposed: exposed(false), Witness: st.Witness}
		switch st.Context {
		case ContextActor:
			r.Performer = &Performer{Process: *o.Actor}
		case ContextSystem:
			r.Performer = &Performer{Process: Process{PID: 900, Created: 90, Token: TokenFacts{SID: systemSID}}}
		case ContextUserImpersonated:
			effective := o.Actor.Token
			r.Performer = &Performer{Process: Process{PID: 901, Created: 91, Token: TokenFacts{SID: systemSID}}, Impersonated: true, Effective: &effective}
		case ContextPeerImpersonated:
			r.Performer = &Performer{Process: Process{PID: 901, Created: 91, Token: TokenFacts{SID: systemSID}}, Impersonated: true,
				Effective: &TokenFacts{SID: sidPeer, Session: 4, ElevationType: elevationTypeDefault, Integrity: "medium", Source: "User32", LogonType: 2,
					AuthenticationID: "00000000:0002a000"}}
		}
		switch st.Expect {
		case ExpectDenied:
			r.Outcome, r.Domain, r.Code = "failed", "win32", accessDenied
		case ExpectRefused:
			r.Outcome, r.Domain, r.Diagnostic = "failed", "product", "admission: "+c.Diagnostic
		}
		o.Steps = append(o.Steps, r)
	}
	return o
}

func TestEvaluateDecidedCasesPass(t *testing.T) {
	l := testLedger(t)
	for _, c := range l.Cases {
		v := Evaluate(l, testRun, passing(l, c))
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
	state := func(o *Observation, stage, role string, f func(*ObjectState)) {
		s := o.States[stage][role]
		f(&s)
		o.States[stage][role] = s
	}
	everyStage := func(o *Observation, role string, f func(*ObjectState)) {
		for _, stage := range stateStages {
			state(o, stage, role, f)
		}
	}
	for _, tc := range []struct {
		name, id, want string
		mutate         func(*Observation)
	}{
		// Path facts and shape.
		{"metadata cleared at every stage", "user-units-nested-junction", "no kind, file ID, owner or DACL", func(o *Observation) {
			everyStage(o, RoleTarget, func(s *ObjectState) { s.FileID, s.Owner, s.DACL, s.Content = "", "", "", "" })
		}},
		{"link metadata cleared", "user-units-file-symlink", "the link at before: no kind, file ID, owner or DACL", func(o *Observation) {
			for _, stage := range []string{stageBefore, stageAfter} {
				state(o, stage, RoleLink, func(s *ObjectState) { s.FileID, s.Owner, s.DACL = "", "", "" })
			}
		}},
		{"failed read", "user-units-nested-junction", "the sibling was not read at after", func(o *Observation) {
			state(o, stageAfter, RoleSibling, func(s *ObjectState) { s.Read = false })
		}},
		{"failed baseline inspection taken as absence", "user-units-file-symlink", "the link was not read at baseline", func(o *Observation) {
			state(o, stageBaseline, RoleLink, func(s *ObjectState) { s.Read = false })
		}},
		{"no content witness", "data-child-rename-standard", "the leaf at after: not the case's directory with a content witness", func(o *Observation) {
			everyStage(o, RoleLeaf, func(s *ObjectState) { s.Content = "" })
		}},
		{"a file as the ancestor", "data-child-rename-standard", "the ancestor at before: not the case's directory", func(o *Observation) {
			everyStage(o, RoleAncestor, func(s *ObjectState) { s.Kind = "file" })
		}},
		{"a file as the renamed directory", "install-root-ancestor-rename-filtered", "the leaf at after: not the case's directory", func(o *Observation) {
			everyStage(o, RoleLeaf, func(s *ObjectState) { s.Kind = "file" })
		}},
		{"a directory as the linked unit file", "user-units-file-symlink", "the target at before: not the case's file", func(o *Observation) {
			everyStage(o, RoleTarget, func(s *ObjectState) { s.Kind = "directory" })
		}},
		{"a file behind a junction", "user-units-nested-junction", "the target at after: not the case's directory", func(o *Observation) {
			everyStage(o, RoleTarget, func(s *ObjectState) { s.Kind = "file" })
		}},
		{"a directory as the peer's regular unit", "user-units-nested-junction", "the sibling at before: not the case's file", func(o *Observation) {
			everyStage(o, RoleSibling, func(s *ObjectState) { s.Kind = "directory" })
		}},
		{"a kind that changes between stages", "data-child-rename-filtered", "the leaf at restored: not the case's directory", func(o *Observation) {
			state(o, stageRestored, RoleLeaf, func(s *ObjectState) { s.Kind = "file" })
		}},
		{"junction for a file-link case", "user-units-file-symlink", "not a file symbolic link", func(o *Observation) {
			for _, stage := range []string{stageBefore, stageAfter} {
				state(o, stage, RoleLink, func(s *ObjectState) { s.Kind, s.ReparseTag = "junction", tagMountPoint })
			}
		}},
		{"symbolic link for a junction case", "user-winunitd-component-junction", "not a junction", func(o *Observation) {
			for _, stage := range []string{stageBefore, stageAfter} {
				state(o, stage, RoleLink, func(s *ObjectState) { s.Kind, s.ReparseTag = "symlink-directory", tagSymlink })
			}
		}},
		{"link to an unwatched target", "user-units-nested-junction", "does not resolve to the watched target", func(o *Observation) {
			for _, stage := range []string{stageBefore, stageAfter} {
				state(o, stage, RoleLink, func(s *ObjectState) { s.ResolvedID = "vol-1:elsewhere" })
			}
		}},
		{"link left after restoration", "user-units-file-symlink", "the link exists at restored", func(o *Observation) {
			o.States[stageRestored][RoleLink] = o.States[stageAfter][RoleLink]
		}},
		{"target changed", "user-units-nested-junction", "the target changed during the steps", func(o *Observation) {
			state(o, stageAfter, RoleTarget, func(s *ObjectState) { s.Content = strings.Repeat("d", 64) })
		}},
		{"ACL not restored", "data-child-rename-filtered", "the target was not restored", func(o *Observation) {
			state(o, stageRestored, RoleTarget, func(s *ObjectState) { s.DACL += "(A;;FA;;;BU)" })
		}},
		{"state read outside its stage", "data-child-rename-standard", "the leaf was read outside its after stage", func(o *Observation) {
			state(o, stageAfter, RoleLeaf, func(s *ObjectState) { s.At = 650 })
		}},
		{"another source", "data-child-rename-standard", "not of the admitted run's source", func(o *Observation) { o.Envelope.Source = strings.Repeat("f", 40) }},
		{"observer not SYSTEM", "data-child-rename-standard", "the observer is not a SYSTEM process", func(o *Observation) { o.Envelope.Observer.Token.SID = sidStandard }},
		{"stages out of order", "data-child-rename-standard", "stage marks are missing or out of order", func(o *Observation) {
			o.Envelope.Stages[2], o.Envelope.Stages[3] = o.Envelope.Stages[3], o.Envelope.Stages[2]
		}},
		{"step outside its stage", "data-child-rename-standard", "step rename-units ran outside the steps stage", func(o *Observation) {
			step(o, "rename-units").At = 450
		}},
		// Ancestor rights.
		{"delete-child on the ancestor", "data-child-rename-standard", "holds a right to remove or rename the leaf", func(o *Observation) {
			state(o, stageBefore, RoleAncestor, func(s *ObjectState) { s.ActorRights = rights(0x4 | rightDeleteKid) })
		}},
		{"delete on the leaf", "data-child-rename-filtered", "holds a right to remove or rename the leaf", func(o *Observation) {
			state(o, stageBefore, RoleLeaf, func(s *ObjectState) { s.ActorRights = rights(rightDelete) })
		}},
		{"rights not measured", "data-child-rename-standard", "effective rights on the ancestor and leaf were not measured", func(o *Observation) {
			state(o, stageBefore, RoleAncestor, func(s *ObjectState) { s.ActorRights = nil })
		}},
		// The actor and the performers.
		{"local service as the account", "user-units-nested-junction", "the actor is not the run's user account", func(o *Observation) {
			o.Actor.Token = TokenFacts{SID: localServ, AuthenticationID: "00000000:000003e5"}
		}},
		{"another account", "data-child-rename-standard", "the actor is not the run's standard account", func(o *Observation) {
			o.Actor.Token.SID = sidPeer
		}},
		{"session-zero token for an interactive account", "user-units-nested-junction", "not a non-elevated interactive logon", func(o *Observation) {
			o.Actor.Token.Session, o.Actor.Token.LogonType = 0, 3
		}},
		{"no logon identity", "data-child-rename-filtered", "elevated or has no logon", func(o *Observation) { o.Actor.Token.AuthenticationID = "" }},
		{"no held actor", "data-child-rename-standard", "no held actor process", func(o *Observation) { o.Actor.PID = 0 }},
		{"run names no account", "data-child-rename-standard", "the run names no standard account", func(o *Observation) {}},
		{"attempt by another process", "data-child-rename-standard", "step rename-units: not performed by the actor's own process", func(o *Observation) {
			step(o, "rename-units").Performer.PID++
		}},
		{"probe not impersonating the account's logon", "user-units-nested-junction", "not impersonating the account's own logon", func(o *Observation) {
			step(o, "delegated-admission").Performer.Effective.AuthenticationID = "00000000:00099000"
		}},
		{"probe as the account's own process", "user-units-file-symlink", "not performed by SYSTEM impersonating an account", func(o *Observation) {
			step(o, "delegated-admission").Performer = &Performer{Process: *o.Actor}
		}},
		{"peer control impersonating the account", "user-units-nested-junction", "not impersonating the run's peer account", func(o *Observation) {
			eff := o.Actor.Token
			step(o, "peer-regular-unit-admitted").Performer.Effective = &eff
		}},
		// Results.
		{"denied attempt exposing the target", "data-child-rename-standard", "step rename-units: the target was exposed", func(o *Observation) {
			step(o, "rename-units").TargetExposed = exposed(true)
		}},
		{"success carrying access denied", "user-units-nested-junction", "a success carries an error", func(o *Observation) {
			r := step(o, "delegated-admission")
			r.Domain, r.Code = "win32", accessDenied
		}},
		{"refusal without a diagnostic", "user-winunitd-component-junction", "a product refusal has no diagnostic", func(o *Observation) {
			step(o, "delegated-admission").Diagnostic = ""
		}},
		{"refusal with another diagnostic", "user-winunitd-component-junction", "expected refused with the product's diagnostic", func(o *Observation) {
			step(o, "delegated-admission").Diagnostic = "admission probe timed out"
		}},
		{"exposure unknown", "user-units-file-symlink", "whether the target was exposed is unknown", func(o *Observation) {
			step(o, "delegated-admission").TargetExposed = nil
		}},
		{"linked unit admitted the account", "user-units-nested-junction", `the consumer's result is "admitted"`, func(o *Observation) {
			step(o, "delegated-admission").Witness = "admitted"
		}},
		{"peer control without its admission", "user-units-file-symlink", `the consumer's result is ""`, func(o *Observation) {
			step(o, "peer-regular-unit-admitted").Witness = ""
		}},
		{"no outcome", "data-child-rename-filtered", "step rename-own-directory: no outcome", func(o *Observation) {
			step(o, "rename-own-directory").Outcome = ""
		}},
		{"failure without a domain", "data-child-rename-standard", "a failure has no error domain", func(o *Observation) {
			step(o, "rename-units").Domain = ""
		}},
		{"denial with another error", "data-child-rename-standard", "expected denied", func(o *Observation) { step(o, "rename-units").Code = 32 }},
		// Identity of the observation.
		{"another ledger", "user-units-nested-junction", "names another ledger", func(o *Observation) { o.Ledger = strings.Repeat("0", 64) }},
		{"schema", "user-units-nested-junction", "observation schema", func(o *Observation) { o.Schema = 1 }},
	} {
		c, ok := l.Case(tc.id)
		if !ok {
			t.Fatalf("%s: no case %s", tc.name, tc.id)
		}
		o := passing(l, c)
		tc.mutate(o)
		run := testRun
		if tc.name == "run names no account" {
			run.Accounts = map[string]string{ActorUser: sidStandard}
		}
		v := Evaluate(l, run, o)
		if v.Pass || !strings.Contains(strings.Join(v.Problems, "\n"), tc.want) {
			t.Errorf("%s: problems %q", tc.name, v.Problems)
		}
	}
	if v := Evaluate(l, testRun, &Observation{Schema: ObservationSchema, Case: "no-such-case", Ledger: l.Digest()}); v.Pass {
		t.Error("an unknown case passed")
	}
}

// An observation is evaluated against the policy it names: a ledger whose
// cases changed, even into an otherwise valid decided case, has another
// digest.
func TestEvaluateBindsTheEvaluatedPolicy(t *testing.T) {
	l := testLedger(t)
	c, _ := l.Case("user-units-nested-junction")
	o := passing(l, c)
	data, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	for i := range changed.Cases {
		if changed.Cases[i].ID == "units-nested-junction-reload" {
			changed.Cases[i].Outcome, changed.Cases[i].Policy, changed.Cases[i].Question, changed.Cases[i].Recommendation = OutcomeNoFollow, "no-recursive-follow", "", ""
		}
	}
	if err := changed.Validate(); err != nil {
		t.Fatal(err)
	}
	if v := Evaluate(changed, testRun, o); v.Pass || !strings.Contains(strings.Join(v.Problems, "\n"), "names another ledger") {
		t.Fatalf("an observation of one policy passed under another: %v", v.Problems)
	}
	if changed.Digest() == l.Digest() {
		t.Fatal("a changed policy has the same digest")
	}
}
