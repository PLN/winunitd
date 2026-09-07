package core

import (
	"context"
	"errors"
	"fmt"
)

// Starter activates a unit. Implementations must not require Windows APIs
// so graph execution can be tested on any GOOS. The manager supplies a
// runtime starter that calls CreateProcess into a per-unit Job Object.
type Starter interface {
	Start(ctx context.Context, name string) error
}

// StartFunc adapts a function to Starter.
type StartFunc func(ctx context.Context, name string) error

// Start calls f.
func (f StartFunc) Start(ctx context.Context, name string) error {
	return f(ctx, name)
}

// StartRejectionObserver optionally receives failures for jobs whose Start was
// never called. Delivery is synchronous on the transaction executor, once per
// rejected job, before dependent jobs are released. It must not wait for work
// that depends on this executor. Adapter completions still belong to Starter.
type StartRejectionObserver interface {
	StartRejected(name string, err error)
}

// Stopper deactivates a unit. KillMode=job (TerminateJobObject) lives in
// the manager; graph execution stays free of Windows APIs.
type Stopper interface {
	Stop(ctx context.Context, name string) error
}

// StopFunc adapts a function to Stopper.
type StopFunc func(ctx context.Context, name string) error

// Stop calls f.
func (f StopFunc) Stop(ctx context.Context, name string) error {
	return f(ctx, name)
}

// DependencyError means a unit failed because a Requires= dependency failed.
type DependencyError struct {
	Unit     string
	Required string
	Err      error
}

func (e *DependencyError) Error() string {
	if e == nil {
		return "dependency failed"
	}
	if e.Err == nil {
		return fmt.Sprintf("%s failed because required %s failed", e.Unit, e.Required)
	}
	return fmt.Sprintf("%s failed because required %s failed: %v", e.Unit, e.Required, e.Err)
}

func (e *DependencyError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Run is the outcome of executing a transaction.
type Run struct {
	States  map[string]State
	Errors  map[string]error
	Started []string // units whose Starter.Start was invoked
	Stopped []string // units whose Stopper.Stop was invoked
}

// StateOf returns the recorded state, or Inactive if name was not in the run.
func (r *Run) StateOf(name string) State {
	if r == nil || r.States == nil {
		return Inactive
	}
	if s, ok := r.States[NormalizeName(name)]; ok {
		return s
	}
	return Inactive
}

// Err returns the failure for name, if any.
func (r *Run) Err(name string) error {
	if r == nil || r.Errors == nil {
		return nil
	}
	return r.Errors[NormalizeName(name)]
}

// Start plans and executes a start transaction. The plan is validated
// before any Starter call.
func (g *Graph) Start(ctx context.Context, starter Starter, names ...string) (*Run, error) {
	tx, err := g.PlanStart(names...)
	if err != nil {
		return nil, err
	}
	return tx.Execute(ctx, starter)
}

// Stop plans and executes a single-unit stop transaction. The named units
// plus reverse Requires=/BindsTo=/PartOf= stop; forward Requires=/Wants=
// and pure After=/Before= neighbors do not (DESIGN.md §10, §42). Independent
// branches stop concurrently. A Stopper error does not skip the rest.
func (g *Graph) Stop(ctx context.Context, stopper Stopper, names ...string) (*Run, error) {
	tx, err := g.PlanStop(names...)
	if err != nil {
		return nil, err
	}
	return tx.ExecuteStop(ctx, stopper)
}

// Shutdown plans and executes the manager-stop transaction: reverse
// After=/Before= over the After-closure of the roots (typically
// shutdown.target plus active units). A Stopper error does not skip
// the rest of the transaction (DESIGN.md §42).
func (g *Graph) Shutdown(ctx context.Context, stopper Stopper, names ...string) (*Run, error) {
	tx, err := g.PlanShutdown(names...)
	if err != nil {
		return nil, err
	}
	return tx.ExecuteStop(ctx, stopper)
}

// DefaultTransactionWorkers bounds concurrent adapter calls within one plan.
// Manager-wide admission is a separate bound.
const DefaultTransactionWorkers = 16

// Execute runs a previously validated transaction. Independent branches
// start concurrently; After= is honored without serializing the whole graph.
func (tx *Transaction) Execute(ctx context.Context, starter Starter) (*Run, error) {
	return tx.ExecuteWithLimit(ctx, starter, DefaultTransactionWorkers)
}

// ExecuteWithLimit preserves dependency ordering while limiting adapter calls.
// Accepted calls are drained even after cancellation; queued starts are canceled.
func (tx *Transaction) ExecuteWithLimit(ctx context.Context, starter Starter, workers int) (*Run, error) {
	if workers < 1 {
		return nil, fmt.Errorf("transaction worker limit must be positive")
	}
	if starter == nil {
		return nil, fmt.Errorf("nil starter")
	}
	if err := tx.validate(); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	names := tx.jobNames()
	ex := &executor{
		tx:        tx,
		starter:   starter,
		ctx:       ctx,
		launched:  make(map[string]bool, len(names)),
		released:  make(map[string]bool, len(names)),
		remaining: make(map[string]int, len(names)),
		unblock:   make(map[string][]string, len(names)),
		run: &Run{
			States: make(map[string]State, len(names)),
			Errors: make(map[string]error, len(names)),
		},
	}
	for _, name := range names {
		ex.run.States[name] = Inactive
		deps := tx.waitsFor[name]
		ex.remaining[name] = len(deps)
		for _, dep := range deps {
			ex.unblock[dep] = append(ex.unblock[dep], name)
		}
	}

	finished := make(chan startResult, min(workers, len(names)))
	for {
		if err := ctx.Err(); err != nil {
			for _, name := range names {
				if ex.run.States[name] == Inactive && !ex.launched[name] {
					ex.markFailed(name, err)
				}
			}
			break
		}
		for _, name := range names {
			if ex.inflight >= workers {
				break
			}
			ex.tryLaunch(name, finished)
		}
		if ex.inflight == 0 {
			break
		}
		res := <-finished
		ex.inflight--
		ex.onFinish(res)
	}
	for ex.inflight > 0 {
		res := <-finished
		ex.inflight--
		ex.onFinish(res)
	}

	for _, name := range names {
		if ex.run.States[name] == Inactive {
			if ex.launched[name] {
				continue // skipped cleanly (e.g. RequiresInteractiveSession)
			}
			ex.markFailed(name, fmt.Errorf("unit %q was not started", name))
		}
	}

	return ex.run, rootError(tx, ex.run)
}

// ExecuteStop runs a previously validated stop transaction.
func (tx *Transaction) ExecuteStop(ctx context.Context, stopper Stopper) (*Run, error) {
	return tx.ExecuteStopWithLimit(ctx, stopper, DefaultTransactionWorkers)
}

// ExecuteStopWithLimit bounds adapter calls while retaining best-effort cleanup
// of all planned members, including after an earlier stop failure.
func (tx *Transaction) ExecuteStopWithLimit(ctx context.Context, stopper Stopper, workers int) (*Run, error) {
	if workers < 1 {
		return nil, fmt.Errorf("transaction worker limit must be positive")
	}
	if stopper == nil {
		return nil, fmt.Errorf("nil stopper")
	}
	if err := tx.validate(); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	names := tx.jobNames()
	ex := &stopExecutor{
		tx:        tx,
		stopper:   stopper,
		ctx:       ctx,
		launched:  make(map[string]bool, len(names)),
		released:  make(map[string]bool, len(names)),
		remaining: make(map[string]int, len(names)),
		unblock:   make(map[string][]string, len(names)),
		run: &Run{
			States: make(map[string]State, len(names)),
			Errors: make(map[string]error, len(names)),
		},
	}
	for _, name := range names {
		ex.run.States[name] = Active
		deps := tx.waitsFor[name]
		ex.remaining[name] = len(deps)
		for _, dep := range deps {
			ex.unblock[dep] = append(ex.unblock[dep], name)
		}
	}

	finished := make(chan startResult, min(workers, len(names)))
	for {
		for _, name := range names {
			if ex.inflight >= workers {
				break
			}
			ex.tryLaunch(name, finished)
		}
		if ex.inflight == 0 {
			break
		}
		res := <-finished
		ex.inflight--
		ex.onFinish(res)
	}
	for ex.inflight > 0 {
		res := <-finished
		ex.inflight--
		ex.onFinish(res)
	}

	for _, name := range names {
		if !ex.launched[name] {
			ex.markDone(name, fmt.Errorf("unit %q was not stopped", name))
		}
	}

	return ex.run, rootError(tx, ex.run)
}

func rootError(tx *Transaction, run *Run) error {
	if tx == nil || run == nil {
		return nil
	}
	for _, root := range tx.roots {
		if err := run.Errors[root]; err != nil {
			return err
		}
	}
	return nil
}

type startResult struct {
	name string
	err  error
}

type executor struct {
	tx        *Transaction
	starter   Starter
	ctx       context.Context
	run       *Run
	launched  map[string]bool
	released  map[string]bool
	remaining map[string]int
	unblock   map[string][]string
	inflight  int
}

func (e *executor) tryLaunch(name string, finished chan<- startResult) {
	if e.launched[name] {
		return
	}
	if e.run.States[name] == Failed {
		return
	}
	if e.remaining[name] > 0 {
		return
	}
	if err := e.requiresFailed(name); err != nil {
		e.markFailed(name, err)
		return
	}
	e.launched[name] = true
	e.run.States[name] = Activating
	e.run.Started = append(e.run.Started, name)
	e.inflight++
	go func(name string) {
		finished <- startResult{name: name, err: e.starter.Start(e.ctx, name)}
	}(name)
}

func (e *executor) requiresFailed(name string) error {
	n := e.tx.g.nodes[name]
	if n == nil {
		return nil
	}
	for _, req := range n.startRequireNames() {
		if _, in := e.tx.jobs[req]; !in {
			continue
		}
		if err := e.run.Errors[req]; err != nil {
			return &DependencyError{Unit: name, Required: req, Err: err}
		}
	}
	return nil
}

func (e *executor) onFinish(res startResult) {
	e.releaseAfter(res.name)
	if res.err != nil {
		if errors.Is(res.err, ErrSkipped) {
			e.run.States[res.name] = Inactive
			return
		}
		e.markFailed(res.name, res.err)
		return
	}
	if e.run.States[res.name] == Failed {
		return
	}
	if err := e.requiresFailed(res.name); err != nil {
		e.markFailed(res.name, err)
		return
	}
	e.run.States[res.name] = Active
}

// jobNotYetStarted reports whether a transaction job has not reached
// Active (queued Inactive, or Activating and not yet active). systemd
// fails not-yet-started jobs on Requires= failure; already-Active
// requirers are left running (issue #35, DESIGN.md §10).
func (e *executor) jobNotYetStarted(name string) bool {
	switch e.run.States[name] {
	case Inactive, Activating:
		return true
	default:
		return false
	}
}

func (e *executor) markFailed(name string, err error) {
	if !e.jobNotYetStarted(name) && e.run.States[name] != Failed {
		// Already Active (or Deactivating): do not record Failed while
		// the process is still running. Auto-stop of live requirers is
		// a separate product call, not DESIGN.md §10 start-failure.
		return
	}
	if _, ok := e.run.Errors[name]; ok {
		e.run.States[name] = Failed
		return
	}
	e.run.Errors[name] = err
	e.run.States[name] = Failed
	if !e.launched[name] {
		if observer, ok := e.starter.(StartRejectionObserver); ok {
			observer.StartRejected(name, err)
		}
		e.releaseAfter(name)
	}
	for _, other := range e.tx.jobNames() {
		if other == name {
			continue
		}
		on := e.tx.g.nodes[other]
		if on == nil {
			continue
		}
		if !on.startRequiresName(name) {
			continue
		}
		if !e.jobNotYetStarted(other) {
			continue
		}
		e.markFailed(other, &DependencyError{Unit: other, Required: name, Err: err})
	}
}

func (e *executor) releaseAfter(name string) {
	if e.released[name] {
		return
	}
	e.released[name] = true
	for _, succ := range e.unblock[name] {
		e.remaining[succ]--
		if e.remaining[succ] < 0 {
			e.remaining[succ] = 0
		}
	}
}

type stopExecutor struct {
	tx        *Transaction
	stopper   Stopper
	ctx       context.Context
	run       *Run
	launched  map[string]bool
	released  map[string]bool
	remaining map[string]int
	unblock   map[string][]string
	inflight  int
}

func (e *stopExecutor) tryLaunch(name string, finished chan<- startResult) {
	if e.launched[name] {
		return
	}
	if e.remaining[name] > 0 {
		return
	}
	e.launched[name] = true
	e.run.States[name] = Deactivating
	e.run.Stopped = append(e.run.Stopped, name)
	e.inflight++
	go func(name string) {
		finished <- startResult{name: name, err: e.stopper.Stop(e.ctx, name)}
	}(name)
}

func (e *stopExecutor) onFinish(res startResult) {
	e.releaseAfter(res.name)
	e.markDone(res.name, res.err)
}

func (e *stopExecutor) markDone(name string, err error) {
	if err != nil {
		if _, ok := e.run.Errors[name]; !ok {
			e.run.Errors[name] = err
		}
	}
	e.run.States[name] = Inactive
	e.releaseAfter(name)
}

func (e *stopExecutor) releaseAfter(name string) {
	if e.released[name] {
		return
	}
	e.released[name] = true
	for _, succ := range e.unblock[name] {
		e.remaining[succ]--
		if e.remaining[succ] < 0 {
			e.remaining[succ] = 0
		}
	}
}
