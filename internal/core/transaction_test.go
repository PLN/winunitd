package core

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/unit"
)

func TestFailurePropagation(t *testing.T) {
	t.Parallel()

	fail := errors.New("start failed")

	tests := []struct {
		name         string
		units        []*unit.Unit
		start        string
		fail         []string
		wantStarted  []string
		wantActive   []string
		wantFailed   []string
		wantRootErr  bool
		wantDepErrOn string
	}{
		{
			name: "Requires failure fails the depender",
			units: []*unit.Unit{
				{Name: "web.service", Requires: []string{"db.service"}, After: []string{"db.service"}},
				{Name: "db.service"},
			},
			start:        "web.service",
			fail:         []string{"db.service"},
			wantStarted:  []string{"db.service"},
			wantFailed:   []string{"db.service", "web.service"},
			wantRootErr:  true,
			wantDepErrOn: "web.service",
		},
		{
			name: "Wants failure does not fail the depender",
			units: []*unit.Unit{
				{Name: "web.service", Wants: []string{"cache.service"}, After: []string{"cache.service"}},
				{Name: "cache.service"},
			},
			start:       "web.service",
			fail:        []string{"cache.service"},
			wantStarted: []string{"cache.service", "web.service"},
			wantActive:  []string{"web.service"},
			wantFailed:  []string{"cache.service"},
			wantRootErr: false,
		},
		{
			name: "After without Requires does not start the dependency",
			units: []*unit.Unit{
				{Name: "web.service", After: []string{"db.service"}},
				{Name: "db.service"},
			},
			start:       "web.service",
			wantStarted: []string{"web.service"},
			wantActive:  []string{"web.service"},
			wantRootErr: false,
		},
		{
			name: "Requires failure propagates through a chain",
			units: []*unit.Unit{
				{Name: "web.service", Requires: []string{"app.service"}, After: []string{"app.service"}},
				{Name: "app.service", Requires: []string{"db.service"}, After: []string{"db.service"}},
				{Name: "db.service"},
			},
			start:        "web.service",
			fail:         []string{"db.service"},
			wantStarted:  []string{"db.service"},
			wantFailed:   []string{"app.service", "db.service", "web.service"},
			wantRootErr:  true,
			wantDepErrOn: "web.service",
		},
		{
			name: "Wants of a failed Requires unit does not fail the original root",
			units: []*unit.Unit{
				{Name: "web.service", Wants: []string{"helper.service"}},
				{Name: "helper.service", Requires: []string{"disk.service"}, After: []string{"disk.service"}},
				{Name: "disk.service"},
			},
			start:       "web.service",
			fail:        []string{"disk.service"},
			wantStarted: []string{"disk.service", "web.service"},
			wantActive:  []string{"web.service"},
			wantFailed:  []string{"disk.service", "helper.service"},
			wantRootErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := mustBuild(t, tt.units...)
			failSet := make(map[string]error, len(tt.fail))
			for _, name := range tt.fail {
				failSet[name] = fail
			}
			rec := &recordingStarter{fail: failSet}
			run, err := g.Start(context.Background(), rec, tt.start)
			if tt.wantRootErr {
				if err == nil {
					t.Fatal("expected root error")
				}
			} else if err != nil {
				t.Fatalf("unexpected root error: %v", err)
			}
			if run == nil {
				t.Fatal("run is nil")
			}
			if tt.wantStarted != nil && !equalSorted(run.Started, tt.wantStarted) {
				t.Fatalf("started = %v, want %v", run.Started, tt.wantStarted)
			}
			for _, name := range tt.wantActive {
				if run.StateOf(name) != Active {
					t.Fatalf("%s state = %s, want active (err=%v)", name, run.StateOf(name), run.Err(name))
				}
			}
			for _, name := range tt.wantFailed {
				if run.StateOf(name) != Failed {
					t.Fatalf("%s state = %s, want failed", name, run.StateOf(name))
				}
			}
			if tt.wantDepErrOn != "" {
				var dep *DependencyError
				if !errors.As(run.Err(tt.wantDepErrOn), &dep) {
					t.Fatalf("%s error = %v, want DependencyError", tt.wantDepErrOn, run.Err(tt.wantDepErrOn))
				}
			}
			if containsName(tt.wantStarted, "db.service") == false && strings.Contains(tt.name, "After without") {
				if rec.called("db.service") {
					t.Fatal("db.service must not be started")
				}
			}
		})
	}
}

// TestRequiresFailureFailsNotYetStartedWithoutAfter pins systemd-style
// job failure: Requires= without After= still fails a depender that has
// not reached Active (queued or still Activating). web.Start is held
// until db has failed (canary After=db starts only after that), so web
// is Activating, not Active, when failure propagates.
func TestRequiresFailureFailsNotYetStartedWithoutAfter(t *testing.T) {
	t.Parallel()
	g := mustBuild(t,
		&unit.Unit{Name: "web.service", Requires: []string{"db.service"}},
		&unit.Unit{Name: "db.service"},
		&unit.Unit{Name: "canary.service", After: []string{"db.service"}},
	)
	releaseWeb := make(chan struct{})
	canaryStarted := make(chan struct{})
	st := StartFunc(func(ctx context.Context, name string) error {
		switch name {
		case "db.service":
			return errors.New("start failed")
		case "web.service":
			select {
			case <-releaseWeb:
			case <-ctx.Done():
				return ctx.Err()
			}
			return nil
		case "canary.service":
			close(canaryStarted)
			return nil
		}
		return nil
	})
	done := make(chan resultAndErr, 1)
	go func() {
		run, err := g.Start(context.Background(), st, "web.service", "canary.service")
		done <- resultAndErr{run, err}
	}()
	select {
	case <-canaryStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for db failure to unblock canary")
	}
	close(releaseWeb)
	var got resultAndErr
	select {
	case got = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("execute timed out")
	}
	if got.err == nil {
		t.Fatal("expected root error")
	}
	run := got.run
	if run.StateOf("db.service") != Failed {
		t.Fatalf("db state = %s", run.StateOf("db.service"))
	}
	if run.StateOf("web.service") != Failed {
		t.Fatalf("web state = %s, want failed (not yet Active)", run.StateOf("web.service"))
	}
	var dep *DependencyError
	if !errors.As(run.Err("web.service"), &dep) {
		t.Fatalf("web error = %v, want DependencyError", run.Err("web.service"))
	}
}

// TestRequiresFailureLeavesAlreadyActiveDependent is the gap versus the
// old "Requires without After still fails the depender" row: if the
// depender has already reached Active, a later Requires= failure must
// not flip it to Failed while it keeps running. A queued requirer in
// the same transaction still goes Failed (issue #35).
func TestRequiresFailureLeavesAlreadyActiveDependent(t *testing.T) {
	t.Parallel()
	g := mustBuild(t,
		&unit.Unit{
			Name:     "app.target",
			Requires: []string{"web.service", "late.service"},
			After:    []string{"web.service", "late.service"},
		},
		&unit.Unit{Name: "web.service", Requires: []string{"db.service"}},
		&unit.Unit{Name: "late.service", Requires: []string{"db.service"}, After: []string{"db.service"}},
		&unit.Unit{Name: "db.service"},
		&unit.Unit{Name: "probe.service", After: []string{"web.service"}},
	)
	probeStarted := make(chan struct{})
	var mu sync.Mutex
	var started []string
	st := StartFunc(func(ctx context.Context, name string) error {
		switch name {
		case "probe.service":
			close(probeStarted)
		case "db.service":
			select {
			case <-probeStarted:
			case <-ctx.Done():
				return ctx.Err()
			}
			return errors.New("start failed")
		case "late.service":
			t.Errorf("late.service must not start after db failed")
			return nil
		}
		mu.Lock()
		started = append(started, name)
		mu.Unlock()
		return nil
	})
	run, err := g.Start(context.Background(), st, "app.target", "probe.service")
	if err == nil {
		t.Fatal("expected root error (app.target not yet started)")
	}
	if run.StateOf("web.service") != Active {
		t.Fatalf("web state = %s, want active (already started)", run.StateOf("web.service"))
	}
	if run.Err("web.service") != nil {
		t.Fatalf("web must not record an error: %v", run.Err("web.service"))
	}
	if run.StateOf("db.service") != Failed {
		t.Fatalf("db state = %s, want failed", run.StateOf("db.service"))
	}
	if run.StateOf("late.service") != Failed {
		t.Fatalf("late state = %s, want failed (not yet started)", run.StateOf("late.service"))
	}
	var dep *DependencyError
	if !errors.As(run.Err("late.service"), &dep) {
		t.Fatalf("late error = %v, want DependencyError", run.Err("late.service"))
	}
	if run.StateOf("app.target") != Failed {
		t.Fatalf("app state = %s, want failed (queued behind After=)", run.StateOf("app.target"))
	}
	if containsName(run.Started, "late.service") {
		t.Fatal("late.service Starter must not be invoked")
	}
	if !containsName(run.Started, "web.service") {
		t.Fatal("web.service must have started (running)")
	}
}

func TestTransactionValidatedBeforeExecute(t *testing.T) {
	t.Parallel()

	t.Run("ordering cycle starts nothing", func(t *testing.T) {
		t.Parallel()
		g := mustBuild(t,
			&unit.Unit{Name: "a.service", After: []string{"b.service"}},
			&unit.Unit{Name: "b.service", After: []string{"c.service"}},
			&unit.Unit{Name: "c.service", After: []string{"a.service"}},
		)
		rec := &recordingStarter{}
		run, err := g.Start(context.Background(), rec, "a.service", "b.service", "c.service")
		if run != nil {
			t.Fatalf("run = %+v, want nil on invalid plan", run)
		}
		var c *CycleError
		if !errors.As(err, &c) {
			t.Fatalf("err = %v", err)
		}
		if rec.n() != 0 {
			t.Fatalf("started %d units on an invalid transaction", rec.n())
		}
	})

	t.Run("missing Requires starts nothing", func(t *testing.T) {
		t.Parallel()
		g := mustBuild(t, &unit.Unit{Name: "web.service", Requires: []string{"db.service"}})
		rec := &recordingStarter{}
		run, err := g.Start(context.Background(), rec, "web.service")
		if run != nil {
			t.Fatalf("run = %+v", run)
		}
		var miss *MissingUnitError
		if !errors.As(err, &miss) {
			t.Fatalf("err = %v", err)
		}
		if rec.n() != 0 {
			t.Fatalf("started %d units on an invalid transaction", rec.n())
		}
	})
}

func TestIndependentBranchesScheduledConcurrently(t *testing.T) {
	t.Parallel()

	g := mustBuild(t,
		&unit.Unit{
			Name:     "api.service",
			Requires: []string{"database.service", "redis.service"},
			After:    []string{"database.service", "redis.service"},
		},
		&unit.Unit{Name: "database.service"},
		&unit.Unit{Name: "redis.service"},
	)

	tx, err := g.PlanStart("api.service")
	if err != nil {
		t.Fatal(err)
	}
	if !equalNames(tx.Ready(), "database.service", "redis.service") {
		t.Fatalf("ready = %v; independent branches must be startable together", tx.Ready())
	}
	if containsName(tx.Ready(), "api.service") {
		t.Fatal("api.service must wait for both branches")
	}

	var (
		inDB    atomic.Int32
		inRedis atomic.Int32
		inAPI   atomic.Int32
	)
	release := make(chan struct{})
	st := StartFunc(func(ctx context.Context, name string) error {
		switch name {
		case "database.service":
			inDB.Store(1)
			<-release
		case "redis.service":
			inRedis.Store(1)
			<-release
		case "api.service":
			inAPI.Store(1)
		}
		return nil
	})

	done := make(chan resultAndErr, 1)
	go func() {
		run, err := tx.Execute(context.Background(), st)
		done <- resultAndErr{run, err}
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if inDB.Load() == 1 && inRedis.Load() == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if inDB.Load() != 1 || inRedis.Load() != 1 {
		t.Fatal("database and redis were not started concurrently (graph was serialized)")
	}
	if inAPI.Load() != 0 {
		t.Fatal("api.service started before both dependencies finished")
	}
	close(release)

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.run.StateOf("api.service") != Active {
			t.Fatalf("api state = %s", got.run.StateOf("api.service"))
		}
		if inAPI.Load() != 1 {
			t.Fatal("api.service did not start")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("execute timed out")
	}
}

func TestReadyDoesNotSerializeWholeGraph(t *testing.T) {
	t.Parallel()
	// Two independent chains that join at api:
	//   db -> app,  redis -> cache,  then api after both leaves.
	g := mustBuild(t,
		&unit.Unit{Name: "db.service"},
		&unit.Unit{Name: "app.service", Requires: []string{"db.service"}, After: []string{"db.service"}},
		&unit.Unit{Name: "redis.service"},
		&unit.Unit{Name: "cache.service", Requires: []string{"redis.service"}, After: []string{"redis.service"}},
		&unit.Unit{
			Name:     "api.service",
			Requires: []string{"app.service", "cache.service"},
			After:    []string{"app.service", "cache.service"},
		},
	)
	tx, err := g.PlanStart("api.service")
	if err != nil {
		t.Fatal(err)
	}
	if !equalNames(tx.Ready(), "db.service", "redis.service") {
		t.Fatalf("ready = %v", tx.Ready())
	}
	if len(tx.Ready()) < 2 {
		t.Fatal("independent branches were serialized into a single ready unit")
	}
}

func TestWantsAfterWaitsThenContinues(t *testing.T) {
	t.Parallel()
	g := mustBuild(t,
		&unit.Unit{Name: "web.service", Wants: []string{"cache.service"}, After: []string{"cache.service"}},
		&unit.Unit{Name: "cache.service"},
	)
	var mu sync.Mutex
	var order []string
	st := StartFunc(func(ctx context.Context, name string) error {
		mu.Lock()
		order = append(order, name)
		mu.Unlock()
		if name == "cache.service" {
			return errors.New("cache down")
		}
		return nil
	})
	run, err := g.Start(context.Background(), st, "web.service")
	if err != nil {
		t.Fatal(err)
	}
	if run.StateOf("web.service") != Active {
		t.Fatalf("web state = %s", run.StateOf("web.service"))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "cache.service" || order[1] != "web.service" {
		t.Fatalf("order = %v", order)
	}
}

func TestSkippedUnitStaysInactive(t *testing.T) {
	t.Parallel()
	g := mustBuild(t, &unit.Unit{Name: "gui.service"})
	rec := &recordingStarter{fail: map[string]error{"gui.service": ErrSkipped}}
	run, err := g.Start(context.Background(), rec, "gui.service")
	if err != nil {
		t.Fatalf("skip must not fail the transaction: %v", err)
	}
	if run.StateOf("gui.service") != Inactive {
		t.Fatalf("state = %s, want inactive", run.StateOf("gui.service"))
	}
	if run.Err("gui.service") != nil {
		t.Fatalf("skip must not record an error: %v", run.Err("gui.service"))
	}
}

type recordingStarter struct {
	fail map[string]error
	mu   sync.Mutex
	seq  []string
}

func (s *recordingStarter) Start(ctx context.Context, name string) error {
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

func (s *recordingStarter) n() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.seq)
}

func (s *recordingStarter) called(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return containsName(s.seq, name)
}

type resultAndErr struct {
	run *Run
	err error
}

func equalSorted(got, want []string) bool {
	g := append([]string(nil), got...)
	w := append([]string(nil), want...)
	sort.Strings(g)
	sort.Strings(w)
	return equalNames(g, w...)
}
