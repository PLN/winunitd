package pathcases

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func testLedger(t *testing.T) *Ledger {
	t.Helper()
	l, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestLedgerLoads(t *testing.T) {
	l := testLedger(t)
	decided := 0
	for _, c := range l.Cases {
		if c.Outcome != OutcomeDecisionNeeded {
			decided++
		}
	}
	if len(l.Cases) < 10 || decided == 0 || decided == len(l.Cases) {
		t.Fatalf("%d cases, %d decided", len(l.Cases), decided)
	}
}

// Every policy a case rests on is a heading in a product document.
func TestLedgerPoliciesCiteProductDocuments(t *testing.T) {
	root := moduleRoot(t)
	for name, p := range testLedger(t).Policies {
		file, anchor, _ := strings.Cut(p.Source, "#")
		f, err := os.Open(filepath.Join(root, filepath.FromSlash(file)))
		if err != nil {
			t.Errorf("policy %s: %v", name, err)
			continue
		}
		found := false
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if heading, ok := strings.CutPrefix(sc.Text(), "#"); ok && headingAnchor(strings.TrimLeft(heading, "# ")) == anchor {
				found = true
			}
		}
		f.Close()
		if !found {
			t.Errorf("policy %s: %s has no heading %s", name, file, anchor)
		}
	}
}

var anchorDrop = regexp.MustCompile(`[^a-z0-9 _-]`)

// headingAnchor is GitHub's anchor for a heading.
func headingAnchor(heading string) string {
	return strings.ReplaceAll(anchorDrop.ReplaceAllString(strings.ToLower(strings.TrimSpace(heading)), ""), " ", "-")
}

// The ledger does not repeat the qualified immediate-child, data-root,
// known-folder or daemon-log shapes.
func TestLedgerDoesNotRepeatQualifiedShapes(t *testing.T) {
	for _, c := range testLedger(t).Cases {
		switch {
		case c.Consumer == "preflight" && c.Depth < 2:
			t.Errorf("%s: preflight of an immediate child or the root is qualified", c.ID)
		case c.Tree == "daemon":
			t.Errorf("%s: the daemon-log chain is qualified", c.ID)
		case c.Tree == "user-root" && c.Path != "winunitd":
			t.Errorf("%s: only the winunitd component is new under the user's root", c.ID)
		case c.Shape == "ancestor-rename" && c.Depth != 1:
			t.Errorf("%s: an ancestor case renames the leaf in its parent", c.ID)
		}
	}
}

func TestLedgerRefusesInvalidCases(t *testing.T) {
	base := testLedger(t)
	find := func(l *Ledger, id string) *Case {
		for i := range l.Cases {
			if l.Cases[i].ID == id {
				return &l.Cases[i]
			}
		}
		t.Fatalf("no case %s", id)
		return nil
	}
	for name, mutate := range map[string]func(*Ledger){
		"descendant at an immediate child": func(l *Ledger) { find(l, "units-nested-junction-reload").Depth = 1 },
		"no safe sibling control": func(l *Ledger) {
			c := find(l, "user-units-nested-junction")
			c.Steps = c.Steps[:1]
		},
		"a control expected to fail":  func(l *Ledger) { find(l, "user-units-file-symlink").Steps[1].Expect = ExpectDenied },
		"refuse without a diagnostic": func(l *Ledger) { find(l, "user-winunitd-component-junction").Diagnostic = "" },
		"refuse with a following consumer step": func(l *Ledger) {
			c := find(l, "user-winunitd-component-junction")
			c.Steps = append(c.Steps, Step{Name: "listing", By: "consumer", Expect: ExpectNoFollow})
		},
		"protected without a denied attempt": func(l *Ledger) { find(l, "data-child-rename-standard").Steps[0].Expect = ExpectOK },
		"protected by the fixture":           func(l *Ledger) { find(l, "data-child-rename-filtered").Actor = ActorSystemFixture },
		"protected without a policy":         func(l *Ledger) { find(l, "data-child-rename-standard").Policy = "" },
		"no-follow without a policy":         func(l *Ledger) { find(l, "user-units-nested-junction").Policy = "" },
		"decision without a question":        func(l *Ledger) { find(l, "enabled-target-junction-enable").Question = "" },
		"decided case with an open question": func(l *Ledger) { find(l, "user-units-file-symlink").Question = "Is it?" },
		"unknown policy":                     func(l *Ledger) { find(l, "linger-record-link-startup").Policy = "everything" },
		"unknown consumer":                   func(l *Ledger) { find(l, "linger-record-link-startup").Consumer = "broker" },
		"unknown tree":                       func(l *Ledger) { find(l, "linger-record-link-startup").Tree = "anywhere" },
		"no external target role": func(l *Ledger) {
			find(l, "journal-file-link-append").Roles = []string{RoleLink, RoleSibling}
		},
		"fixture state with an actor step": func(l *Ledger) {
			c := find(l, "journal-file-link-append")
			c.Steps = append(c.Steps, Step{Name: "user-write", By: "actor", Expect: ExpectDenied})
		},
		"no filtered twin": func(l *Ledger) {
			for i := range l.Cases {
				if l.Cases[i].ID == "data-child-rename-filtered" {
					l.Cases = append(l.Cases[:i], l.Cases[i+1:]...)
					return
				}
			}
		},
		"duplicate case": func(l *Ledger) { l.Cases = append(l.Cases, l.Cases[0]) },
		"duplicate step": func(l *Ledger) {
			c := find(l, "units-nested-junction-reload")
			c.Steps = append(c.Steps, c.Steps[0])
		},
		"policy source without an anchor": func(l *Ledger) {
			p := l.Policies["delegation-probe"]
			p.Source = "docs/USER-ADMISSION.md"
			l.Policies["delegation-probe"] = p
		},
		"nothing marked as qualified": func(l *Ledger) { l.Qualified = nil },
		"schema":                      func(l *Ledger) { l.Schema = 2 },
	} {
		data, err := json.Marshal(base)
		if err != nil {
			t.Fatal(err)
		}
		var l Ledger
		if err := json.Unmarshal(data, &l); err != nil {
			t.Fatal(err)
		}
		mutate(&l)
		data, err = json.Marshal(&l)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Decode(data); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := Decode([]byte(`{"schema":1,"unexpected":true}`)); err == nil {
		t.Error("an unknown field accepted")
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no module root")
		}
		dir = parent
	}
}
