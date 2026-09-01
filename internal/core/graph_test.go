package core

import (
	"errors"
	"strings"
	"testing"

	"github.com/PLN/winunitd/internal/unit"
)

func TestNormalizeName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in, want string
	}{
		{"foo", "foo.service"},
		{"foo.service", "foo.service"},
		{"foo.timer", "foo.timer"},
		{"foo.target", "foo.target"},
		{"foo.registry", "foo.registry"},
		{"foo.eventlog", "foo.eventlog"},
		{"  bar  ", "bar.service"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := NormalizeName(tt.in); got != tt.want {
			t.Errorf("NormalizeName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestBuildDuplicate(t *testing.T) {
	t.Parallel()
	_, err := Build([]*unit.Unit{
		{Name: "a.service"},
		{Name: "a"},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("err = %v", err)
	}
}

func TestAfterWithoutRequiresDoesNotPull(t *testing.T) {
	t.Parallel()
	g := mustBuild(t,
		&unit.Unit{Name: "web.service", After: []string{"db.service"}},
		&unit.Unit{Name: "db.service"},
	)
	tx, err := g.PlanStart("web.service")
	if err != nil {
		t.Fatal(err)
	}
	if tx.Contains("db.service") {
		t.Fatal("After without Requires must not start the dependency")
	}
	if !equalNames(tx.Units(), "web.service") {
		t.Fatalf("units = %v", tx.Units())
	}
}

func TestBeforeWithoutRequiresDoesNotPull(t *testing.T) {
	t.Parallel()
	g := mustBuild(t,
		&unit.Unit{Name: "setup.service", Before: []string{"web.service"}},
		&unit.Unit{Name: "web.service"},
	)
	tx, err := g.PlanStart("web.service")
	if err != nil {
		t.Fatal(err)
	}
	if tx.Contains("setup.service") {
		t.Fatal("Before without Requires must not start the dependency")
	}
}

func TestRequiresAndWantsPull(t *testing.T) {
	t.Parallel()
	g := mustBuild(t,
		&unit.Unit{
			Name:     "web.service",
			Requires: []string{"db.service"},
			Wants:    []string{"cache.service"},
		},
		&unit.Unit{Name: "db.service"},
		&unit.Unit{Name: "cache.service"},
		&unit.Unit{Name: "other.service"},
	)
	tx, err := g.PlanStart("web.service")
	if err != nil {
		t.Fatal(err)
	}
	if !equalNames(tx.Units(), "cache.service", "db.service", "web.service") {
		t.Fatalf("units = %v", tx.Units())
	}
	if tx.Contains("other.service") {
		t.Fatal("unrelated unit pulled in")
	}
}

func TestMissingRequiresFailsPlan(t *testing.T) {
	t.Parallel()
	g := mustBuild(t, &unit.Unit{Name: "web.service", Requires: []string{"db.service"}})
	_, err := g.PlanStart("web.service")
	var miss *MissingUnitError
	if !errors.As(err, &miss) || miss.Unit != "db.service" {
		t.Fatalf("err = %v", err)
	}
}

func TestMissingWantsIsSkipped(t *testing.T) {
	t.Parallel()
	g := mustBuild(t, &unit.Unit{Name: "web.service", Wants: []string{"cache.service"}})
	tx, err := g.PlanStart("web.service")
	if err != nil {
		t.Fatal(err)
	}
	if tx.Contains("cache.service") {
		t.Fatal("missing Wants should be skipped, not pulled")
	}
}

func TestUnqualifiedNamesResolveToService(t *testing.T) {
	t.Parallel()
	g := mustBuild(t,
		&unit.Unit{Name: "web.service", Requires: []string{"db"}, After: []string{"db"}},
		&unit.Unit{Name: "db.service"},
	)
	if !equalNames(g.Requires("web"), "db.service") {
		t.Fatalf("requires = %v", g.Requires("web"))
	}
	tx, err := g.PlanStart("web")
	if err != nil {
		t.Fatal(err)
	}
	if !tx.Contains("db.service") {
		t.Fatal("unqualified Requires=db should pull db.service")
	}
}

func TestBeforeBecomesOrderingOnTarget(t *testing.T) {
	t.Parallel()
	g := mustBuild(t,
		&unit.Unit{Name: "a.service", Before: []string{"b.service"}},
		&unit.Unit{Name: "b.service"},
	)
	if !equalNames(g.WaitsFor("b.service"), "a.service") {
		t.Fatalf("b waits for = %v", g.WaitsFor("b.service"))
	}
	tx, err := g.PlanStart("a.service", "b.service")
	if err != nil {
		t.Fatal(err)
	}
	if !equalNames(tx.Ready(), "a.service") {
		t.Fatalf("ready = %v", tx.Ready())
	}
}

func TestOrderingCyclePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		units []*unit.Unit
		cycle []string
	}{
		{
			name: "A After B After C After A",
			units: []*unit.Unit{
				{Name: "a.service", After: []string{"b.service"}},
				{Name: "b.service", After: []string{"c.service"}},
				{Name: "c.service", After: []string{"a.service"}},
			},
			cycle: []string{"a.service", "b.service", "c.service"},
		},
		{
			name: "self After",
			units: []*unit.Unit{
				{Name: "loop.service", After: []string{"loop.service"}},
			},
			cycle: []string{"loop.service"},
		},
		{
			name: "Before cycle",
			units: []*unit.Unit{
				{Name: "a.service", Before: []string{"b.service"}},
				{Name: "b.service", Before: []string{"c.service"}},
				{Name: "c.service", Before: []string{"a.service"}},
			},
			cycle: []string{"a.service", "c.service", "b.service"},
		},
		{
			name: "mixed After and Before",
			units: []*unit.Unit{
				{Name: "a.service", After: []string{"b.service"}},
				{Name: "b.service"},
				{Name: "c.service", Before: []string{"b.service"}, After: []string{"a.service"}},
			},
			// a After b, b waits for c (c Before b), c After a → a After b After c After a
			cycle: []string{"a.service", "b.service", "c.service"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := mustBuild(t, tt.units...)
			c := g.OrderingCycle()
			if c == nil {
				t.Fatal("expected ordering cycle")
			}
			if c.Error() == "cycle detected" {
				t.Fatal("cycle path must be explicit, not merely \"cycle detected\"")
			}
			if !sameCycle(c.Path, tt.cycle...) {
				t.Fatalf("path = %v (%s), want rotation of %v", c.Path, c.Error(), tt.cycle)
			}
			for _, name := range tt.cycle {
				if !strings.Contains(c.Error(), name) {
					t.Fatalf("Error() %q missing %s", c.Error(), name)
				}
			}
			if !strings.Contains(c.Error(), "After") {
				t.Fatalf("Error() %q missing After", c.Error())
			}
			if c.Detail() == "" || !strings.Contains(c.Detail(), "After=") {
				t.Fatalf("Detail() = %q", c.Detail())
			}
		})
	}
}

func TestPlanStartReportsCycleWhenAllInTransaction(t *testing.T) {
	t.Parallel()
	g := mustBuild(t,
		&unit.Unit{Name: "a.service", After: []string{"b.service"}},
		&unit.Unit{Name: "b.service", After: []string{"c.service"}},
		&unit.Unit{Name: "c.service", After: []string{"a.service"}},
	)
	_, err := g.PlanStart("a.service", "b.service", "c.service")
	var c *CycleError
	if !errors.As(err, &c) {
		t.Fatalf("err = %v", err)
	}
	if !sameCycle(c.Path, "a.service", "b.service", "c.service") {
		t.Fatalf("path = %v", c.Path)
	}
}

func TestAfterCycleDoesNotAffectIndependentStart(t *testing.T) {
	t.Parallel()
	g := mustBuild(t,
		&unit.Unit{Name: "a.service", After: []string{"b.service"}},
		&unit.Unit{Name: "b.service", After: []string{"a.service"}},
		&unit.Unit{Name: "ok.service"},
	)
	tx, err := g.PlanStart("ok.service")
	if err != nil {
		t.Fatal(err)
	}
	if !equalNames(tx.Units(), "ok.service") {
		t.Fatalf("units = %v", tx.Units())
	}
}

func TestRequiresCycleIsNotOrderingCycle(t *testing.T) {
	t.Parallel()
	g := mustBuild(t,
		&unit.Unit{Name: "a.service", Requires: []string{"b.service"}},
		&unit.Unit{Name: "b.service", Requires: []string{"a.service"}},
	)
	if c := g.OrderingCycle(); c != nil {
		t.Fatalf("Requires cycle is not an After cycle: %v", c)
	}
	tx, err := g.PlanStart("a.service")
	if err != nil {
		t.Fatal(err)
	}
	if !equalNames(tx.Units(), "a.service", "b.service") {
		t.Fatalf("units = %v", tx.Units())
	}
	if !equalNames(tx.Ready(), "a.service", "b.service") {
		t.Fatalf("ready = %v", tx.Ready())
	}
}

func TestGraphFromParsedUnits(t *testing.T) {
	t.Parallel()
	web := mustParse(t, "web.service", `
[Unit]
Description=Web
Requires=db.service
After=db.service
Wants=cache.service

[Service]
ExecStart=C:\Tools\web.exe
WorkingDirectory=C:\Tools
`)
	db := mustParse(t, "db.service", `
[Service]
ExecStart=C:\Tools\db.exe
WorkingDirectory=C:\Tools
`)
	cache := mustParse(t, "cache.service", `
[Service]
ExecStart=C:\Tools\cache.exe
WorkingDirectory=C:\Tools
`)
	g := mustBuild(t, web, db, cache)
	tx, err := g.PlanStart("web.service")
	if err != nil {
		t.Fatal(err)
	}
	if !equalNames(tx.Units(), "cache.service", "db.service", "web.service") {
		t.Fatalf("units = %v", tx.Units())
	}
	if !equalNames(tx.Ready(), "cache.service", "db.service") {
		t.Fatalf("ready = %v", tx.Ready())
	}
}

func mustBuild(t *testing.T, units ...*unit.Unit) *Graph {
	t.Helper()
	g, err := Build(units)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func mustParse(t *testing.T, name, src string) *unit.Unit {
	t.Helper()
	rep := unit.ParseUnit(name, src)
	if rep.HasError() {
		t.Fatalf("parse %s: %v", name, rep.Errors())
	}
	return rep.Unit
}

func equalNames(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func sameCycle(path []string, units ...string) bool {
	if len(units) == 0 || len(path) != len(units)+1 {
		return false
	}
	if path[0] != path[len(path)-1] {
		return false
	}
	n := len(units)
	for start := 0; start < n; start++ {
		ok := true
		for i := 0; i < n; i++ {
			if path[i] != units[(start+i)%n] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}
