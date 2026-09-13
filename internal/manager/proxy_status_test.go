package manager

import (
	"reflect"
	"testing"

	"github.com/PLN/winunitd/internal/protocol"
)

// Exercise status, list and immutable snapshots through the public manager paths.
// This helper is shared by reload/replacement and genuine Windows proxy fixtures.
func assertProxyViews(t *testing.T, m *Manager, name, kind, target string) {
	t.Helper()
	check := func(p *protocol.NativeProxyStatus) {
		t.Helper()
		if kind == "process" {
			if p != nil {
				t.Fatalf("managed process has external proxy metadata: %+v", p)
			}
			return
		}
		if kind == "task" {
			kind = "scheduled-task"
		}
		owner := "windows-scm"
		if kind == "scheduled-task" {
			owner = "windows-task-scheduler"
		}
		if p == nil || p.Kind != kind || p.Target != target || p.Owner != owner || p.OwnsProcessTree || p.CapturesOutput || p.SupportsExecStop || p.SupportsJobLimits || p.ManagesDefinition || !reflect.DeepEqual(p.Capabilities, []string{"query-state", "request-start", "request-stop", "request-restart"}) {
			t.Fatalf("incorrect native proxy contract: %+v", p)
		}
	}
	st, err := m.Status(name)
	if err != nil {
		t.Fatal(err)
	}
	check(st.Unit.NativeProxy)
	list, err := m.ListUnits()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, u := range list.Units {
		if u.Name == name {
			check(u.NativeProxy)
			found = true
		}
	}
	if !found {
		t.Fatal("list omitted proxy")
	}
	snapshot, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	p := snapshotUnit(t, snapshot, name).NativeProxy
	check(p)
	if p != nil {
		p.Target = "caller-mutated"
		p.Capabilities[0] = "caller-mutated"
		next, err := m.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		check(snapshotUnit(t, next, name).NativeProxy)
		check(st.Unit.NativeProxy)
	}
}
