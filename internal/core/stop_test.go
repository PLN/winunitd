package core

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/PLN/winunitd/internal/unit"
)

func TestPlanStopLeavesForwardRequires(t *testing.T) {
	t.Parallel()
	g := mustBuild(t,
		&unit.Unit{Name: "web.service", Requires: []string{"db.service"}, After: []string{"db.service"}},
		&unit.Unit{Name: "db.service"},
	)
	tx, err := g.PlanStop("web.service")
	if err != nil {
		t.Fatal(err)
	}
	if !equalNames(tx.Units(), "web.service") {
		t.Fatalf("units = %v; stopping web must not stop db", tx.Units())
	}
	if tx.Contains("db.service") {
		t.Fatal("forward Requires= must not be pulled into a single-unit stop")
	}
}

func TestPlanStopReverseRequiresDependentsFirst(t *testing.T) {
	t.Parallel()
	g := mustBuild(t,
		&unit.Unit{Name: "web.service", Requires: []string{"db.service"}, After: []string{"db.service"}},
		&unit.Unit{Name: "db.service"},
	)
	tx, err := g.PlanStop("db.service")
	if err != nil {
		t.Fatal(err)
	}
	if !equalNames(tx.Units(), "db.service", "web.service") {
		t.Fatalf("units = %v", tx.Units())
	}
	if !equalNames(tx.Ready(), "web.service") {
		t.Fatalf("ready = %v; web must stop before db", tx.Ready())
	}
	if containsName(tx.Ready(), "db.service") {
		t.Fatal("db.service must wait for reverse requirers to stop")
	}
}

func TestPlanStopTargetDoesNotPullWants(t *testing.T) {
	t.Parallel()
	g := mustBuild(t,
		&unit.Unit{Name: "app.target", Wants: []string{"web.service", "db.service"}},
		&unit.Unit{Name: "web.service", After: []string{"db.service"}},
		&unit.Unit{Name: "db.service"},
		&unit.Unit{Name: "other.service"},
	)
	tx, err := g.PlanStop("app.target")
	if err != nil {
		t.Fatal(err)
	}
	if !equalNames(tx.Units(), "app.target") {
		t.Fatalf("units = %v; Wants= are not reverse requirers", tx.Units())
	}
	if tx.Contains("web.service") || tx.Contains("db.service") || tx.Contains("other.service") {
		t.Fatal("forward Wants= must not be pulled into a single-unit stop")
	}
}

func TestPlanStopTargetStopsPartOf(t *testing.T) {
	t.Parallel()
	g := mustBuild(t,
		&unit.Unit{Name: "app.target"},
		&unit.Unit{Name: "web.service", PartOf: []string{"app.target"}, After: []string{"db.service"}},
		&unit.Unit{Name: "db.service", PartOf: []string{"app.target"}},
		&unit.Unit{Name: "other.service"},
	)
	tx, err := g.PlanStop("app.target")
	if err != nil {
		t.Fatal(err)
	}
	if !equalNames(tx.Units(), "app.target", "db.service", "web.service") {
		t.Fatalf("units = %v", tx.Units())
	}
	if tx.Contains("other.service") {
		t.Fatal("unrelated unit pulled into stop")
	}
	if !equalNames(tx.Ready(), "web.service") {
		t.Fatalf("ready = %v; PartOf= dependents stop before the target", tx.Ready())
	}
}

func TestPlanStopDoesNotPullAfterNeighbors(t *testing.T) {
	t.Parallel()
	g := mustBuild(t,
		&unit.Unit{Name: "x.service"},
		&unit.Unit{Name: "y.service", After: []string{"x.service"}},
	)
	tx, err := g.PlanStop("x.service")
	if err != nil {
		t.Fatal(err)
	}
	if !equalNames(tx.Units(), "x.service") {
		t.Fatalf("units = %v; pure After= must not propagate stop", tx.Units())
	}
	if tx.Contains("y.service") {
		t.Fatal("After= without Requires= must not be pulled into a single-unit stop")
	}
}

func TestPlanStopShutdownTargetDoesNotPullAfter(t *testing.T) {
	t.Parallel()
	g := mustBuild(t,
		&unit.Unit{Name: "shutdown.target"},
		&unit.Unit{Name: "final.service", After: []string{"shutdown.target"}},
	)
	tx, err := g.PlanStop("shutdown.target")
	if err != nil {
		t.Fatal(err)
	}
	if tx.Contains("final.service") {
		t.Fatal("PlanStop of shutdown.target is a single-unit stop, not the shutdown plan")
	}
}

func TestPlanStopBindsToIsReverseRequirer(t *testing.T) {
	t.Parallel()
	g := mustBuild(t,
		&unit.Unit{Name: "session.service", BindsTo: []string{"seat.service"}},
		&unit.Unit{Name: "seat.service"},
	)
	tx, err := g.PlanStop("seat.service")
	if err != nil {
		t.Fatal(err)
	}
	if !equalNames(tx.Units(), "seat.service", "session.service") {
		t.Fatalf("units = %v", tx.Units())
	}
	if !equalNames(tx.Ready(), "session.service") {
		t.Fatalf("ready = %v; BindsTo= dependent stops first", tx.Ready())
	}
}

func TestPlanStopReverseRequiresIsTransitive(t *testing.T) {
	t.Parallel()
	g := mustBuild(t,
		&unit.Unit{Name: "api.service", Requires: []string{"web.service"}},
		&unit.Unit{Name: "web.service", Requires: []string{"db.service"}},
		&unit.Unit{Name: "db.service"},
	)
	tx, err := g.PlanStop("db.service")
	if err != nil {
		t.Fatal(err)
	}
	if !equalNames(tx.Units(), "api.service", "db.service", "web.service") {
		t.Fatalf("units = %v", tx.Units())
	}
}

func TestPlanShutdownPullsAfterRootWithoutRequires(t *testing.T) {
	t.Parallel()
	g := mustBuild(t,
		&unit.Unit{Name: "shutdown.target"},
		&unit.Unit{Name: "final.service", After: []string{"shutdown.target"}},
	)
	tx, err := g.PlanShutdown("shutdown.target")
	if err != nil {
		t.Fatal(err)
	}
	if !tx.Contains("final.service") {
		t.Fatal("shutdown must stop everything After= shutdown.target")
	}
	if !equalNames(tx.Ready(), "final.service") {
		t.Fatalf("ready = %v; After= units stop first", tx.Ready())
	}
}

func TestPlanShutdownTargetPullsWants(t *testing.T) {
	t.Parallel()
	g := mustBuild(t,
		&unit.Unit{Name: "app.target", Wants: []string{"web.service", "db.service"}},
		&unit.Unit{Name: "web.service", After: []string{"db.service"}},
		&unit.Unit{Name: "db.service"},
		&unit.Unit{Name: "other.service"},
	)
	tx, err := g.PlanShutdown("app.target")
	if err != nil {
		t.Fatal(err)
	}
	if !equalNames(tx.Units(), "app.target", "db.service", "web.service") {
		t.Fatalf("units = %v", tx.Units())
	}
	if tx.Contains("other.service") {
		t.Fatal("unrelated unit pulled into shutdown")
	}
}

func TestStopExecutesReverseRequirersFirst(t *testing.T) {
	t.Parallel()
	g := mustBuild(t,
		&unit.Unit{Name: "web.service", Requires: []string{"db.service"}, After: []string{"db.service"}},
		&unit.Unit{Name: "db.service"},
	)
	rec := &recordingStopper{}
	run, err := g.Stop(context.Background(), rec, "db.service")
	if err != nil {
		t.Fatal(err)
	}
	if run.StateOf("web.service") != Inactive || run.StateOf("db.service") != Inactive {
		t.Fatalf("states web=%s db=%s", run.StateOf("web.service"), run.StateOf("db.service"))
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.seq) != 2 || rec.seq[0] != "web.service" || rec.seq[1] != "db.service" {
		t.Fatalf("stop order = %v", rec.seq)
	}
}

func TestStopLeavesForwardRequiresActive(t *testing.T) {
	t.Parallel()
	g := mustBuild(t,
		&unit.Unit{Name: "web.service", Requires: []string{"db.service"}, After: []string{"db.service"}},
		&unit.Unit{Name: "db.service"},
	)
	rec := &recordingStopper{}
	run, err := g.Stop(context.Background(), rec, "web.service")
	if err != nil {
		t.Fatal(err)
	}
	if run.StateOf("web.service") != Inactive {
		t.Fatalf("web state = %s", run.StateOf("web.service"))
	}
	if rec.called("db.service") {
		t.Fatal("db.service stopper must not be invoked")
	}
	if txContains(run, "db.service") {
		t.Fatal("db must not be in the stop run")
	}
}

func TestStopContinuesAfterStopperError(t *testing.T) {
	t.Parallel()
	g := mustBuild(t,
		&unit.Unit{Name: "web.service", Requires: []string{"db.service"}, After: []string{"db.service"}},
		&unit.Unit{Name: "db.service"},
	)
	boom := errors.New("job kill failed")
	rec := &recordingStopper{fail: map[string]error{"web.service": boom}}
	run, err := g.Stop(context.Background(), rec, "db.service")
	if err != nil {
		t.Fatalf("db is the root and its stopper succeeded: %v", err)
	}
	if run.Err("web.service") == nil {
		t.Fatal("web stopper error should be recorded")
	}
	if run.StateOf("db.service") != Inactive {
		t.Fatalf("db state = %s; stop must continue after a stopper error", run.StateOf("db.service"))
	}
	if !rec.called("db.service") {
		t.Fatal("db.service must still be stopped")
	}
}

func TestShutdownExecutesReverseAfterOrder(t *testing.T) {
	t.Parallel()
	g := mustBuild(t,
		&unit.Unit{Name: "web.service", Requires: []string{"db.service"}, After: []string{"db.service"}},
		&unit.Unit{Name: "db.service"},
		&unit.Unit{Name: "shutdown.target"},
	)
	rec := &recordingStopper{}
	run, err := g.Shutdown(context.Background(), rec, "shutdown.target", "web.service", "db.service")
	if err != nil {
		t.Fatal(err)
	}
	if run.StateOf("web.service") != Inactive || run.StateOf("db.service") != Inactive {
		t.Fatalf("states web=%s db=%s", run.StateOf("web.service"), run.StateOf("db.service"))
	}
	if !rec.called("web.service") || !rec.called("db.service") {
		t.Fatalf("shutdown stopped %v", rec.seq)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	webAt, dbAt := indexOf(rec.seq, "web.service"), indexOf(rec.seq, "db.service")
	if webAt < 0 || dbAt < 0 || webAt > dbAt {
		t.Fatalf("stop order = %v; want web before db", rec.seq)
	}
}

func TestPlanStopMissingRoot(t *testing.T) {
	t.Parallel()
	g := mustBuild(t, &unit.Unit{Name: "db.service"})
	_, err := g.PlanStop("web.service")
	var miss *MissingUnitError
	if !errors.As(err, &miss) {
		t.Fatalf("err = %v", err)
	}
}

func txContains(run *Run, name string) bool {
	if run == nil || run.States == nil {
		return false
	}
	_, ok := run.States[NormalizeName(name)]
	return ok
}

func indexOf(seq []string, name string) int {
	for i, s := range seq {
		if s == name {
			return i
		}
	}
	return -1
}

type recordingStopper struct {
	fail map[string]error
	mu   sync.Mutex
	seq  []string
}

func (s *recordingStopper) Stop(ctx context.Context, name string) error {
	s.mu.Lock()
	s.seq = append(s.seq, name)
	s.mu.Unlock()
	if s.fail != nil {
		if err, ok := s.fail[name]; ok {
			return err
		}
	}
	return nil
}

func (s *recordingStopper) called(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return containsName(s.seq, name)
}
