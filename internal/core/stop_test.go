package core

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/PLN/winunitd/internal/unit"
)

func TestPlanStopReversesAfter(t *testing.T) {
	t.Parallel()
	g := mustBuild(t,
		&unit.Unit{Name: "web.service", Requires: []string{"db.service"}, After: []string{"db.service"}},
		&unit.Unit{Name: "db.service"},
	)
	tx, err := g.PlanStop("web.service")
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
		t.Fatal("db.service must wait for web to stop")
	}
}

func TestPlanStopTargetPullsWants(t *testing.T) {
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
	if !equalNames(tx.Units(), "app.target", "db.service", "web.service") {
		t.Fatalf("units = %v", tx.Units())
	}
	if tx.Contains("other.service") {
		t.Fatal("unrelated unit pulled into stop")
	}
	if !equalNames(tx.Ready(), "web.service") && !containsName(tx.Ready(), "web.service") {
		t.Fatalf("ready = %v", tx.Ready())
	}
}

func TestPlanStopAfterRootWithoutRequires(t *testing.T) {
	t.Parallel()
	g := mustBuild(t,
		&unit.Unit{Name: "shutdown.target"},
		&unit.Unit{Name: "final.service", After: []string{"shutdown.target"}},
	)
	tx, err := g.PlanStop("shutdown.target")
	if err != nil {
		t.Fatal(err)
	}
	if !tx.Contains("final.service") {
		t.Fatal("stop everything After= shutdown.target")
	}
	if !equalNames(tx.Ready(), "final.service") {
		t.Fatalf("ready = %v; After= units stop first", tx.Ready())
	}
}

func TestStopExecutesReverseOrder(t *testing.T) {
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
	if run.StateOf("web.service") != Inactive || run.StateOf("db.service") != Inactive {
		t.Fatalf("states web=%s db=%s", run.StateOf("web.service"), run.StateOf("db.service"))
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.seq) != 2 || rec.seq[0] != "web.service" || rec.seq[1] != "db.service" {
		t.Fatalf("stop order = %v", rec.seq)
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
	run, err := g.Stop(context.Background(), rec, "web.service")
	if err == nil {
		t.Fatal("expected root error")
	}
	if run.StateOf("db.service") != Inactive {
		t.Fatalf("db state = %s; stop must continue after a stopper error", run.StateOf("db.service"))
	}
	if !rec.called("db.service") {
		t.Fatal("db.service must still be stopped")
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
