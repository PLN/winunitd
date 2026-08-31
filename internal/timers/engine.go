package timers

import (
	"container/heap"
	"sync"
	"time"
)

// maxWait caps how long the engine sleeps before recomputing deadlines
// so calendar timers recover from clock/DST changes (DESIGN.md §18).
const maxWait = 30 * time.Second

// FireFunc is invoked when a timer elapses. It must not call back into
// Engine while holding locks that Engine.Arm/Disarm need, except through
// the documented Engine methods after it returns.
type FireFunc func(name string)

// Engine is an internal timer scheduler (not Task Scheduler).
type Engine struct {
	clk   Clock
	store *Store
	fire  FireFunc

	mu      sync.Mutex
	armed   map[string]*armed
	pq      deadlineHeap
	wakeup  chan struct{}
	stop    chan struct{}
	stopped chan struct{}
	running bool
}

type armed struct {
	spec Spec
	rt   Runtime
	next time.Time
	ok   bool
	gen  uint64
}

// NewEngine starts a scheduler goroutine. Stop it with Stop.
func NewEngine(clk Clock, store *Store, fire FireFunc) *Engine {
	if clk.Now == nil {
		clk.Now = time.Now
	}
	if clk.SinceBoot == nil {
		clk.SinceBoot = platformSinceBoot
	}
	if clk.Startup.IsZero() {
		clk.Startup = clk.Now()
	}
	if store == nil {
		store, _ = OpenStore("")
	}
	e := &Engine{
		clk:     clk,
		store:   store,
		fire:    fire,
		armed:   make(map[string]*armed),
		wakeup:  make(chan struct{}, 1),
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	heap.Init(&e.pq)
	e.running = true
	go e.loop()
	return e
}

// Clock returns the engine clock.
func (e *Engine) Clock() Clock {
	if e == nil {
		return Clock{}
	}
	return e.clk
}

// Stop ends the scheduler goroutine. It is idempotent.
func (e *Engine) Stop() {
	if e == nil {
		return
	}
	e.mu.Lock()
	if !e.running {
		e.mu.Unlock()
		return
	}
	e.running = false
	e.mu.Unlock()
	close(e.stop)
	<-e.stopped
}

func (e *Engine) loop() {
	defer close(e.stopped)
	timer := time.NewTimer(maxWait)
	defer timer.Stop()
	for {
		wait := e.waitDuration()
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(wait)
		select {
		case <-e.stop:
			return
		case <-e.wakeup:
			continue
		case <-timer.C:
			e.fireDue()
		}
	}
}

func (e *Engine) waitDuration() time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.clk.now()
	e.dropStaleLocked()
	if e.pq.Len() == 0 {
		return maxWait
	}
	when := e.pq[0].when
	d := when.Sub(now)
	if d < 0 {
		return 0
	}
	if d > maxWait {
		return maxWait
	}
	return d
}

func (e *Engine) fireDue() {
	now := e.clk.now()
	for {
		name, scheduled, ok := e.popDue(now)
		if !ok {
			return
		}
		e.consume(name, scheduled, now)
	}
}

func (e *Engine) popDue(now time.Time) (name string, scheduled time.Time, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.dropStaleLocked()
	if e.pq.Len() == 0 {
		return "", time.Time{}, false
	}
	it := e.pq[0]
	a := e.armed[it.name]
	if a == nil || it.gen != a.gen {
		heap.Pop(&e.pq)
		return "", time.Time{}, false
	}
	if it.when.After(now) {
		return "", time.Time{}, false
	}
	heap.Pop(&e.pq)
	return it.name, it.when, true
}

func (e *Engine) consume(name string, scheduled, actual time.Time) {
	e.mu.Lock()
	a := e.armed[name]
	if a == nil {
		e.mu.Unlock()
		return
	}
	MarkFired(a.spec, &a.rt, e.clk, scheduled, actual)
	_ = e.store.Save(name, a.rt)
	fire := e.fire
	e.mu.Unlock()

	if fire != nil {
		go fire(name)
	}

	e.mu.Lock()
	if a2 := e.armed[name]; a2 == a {
		e.rescheduleLocked(a)
	}
	e.mu.Unlock()
	e.kick()
}

func (e *Engine) dropStaleLocked() {
	for e.pq.Len() > 0 {
		it := e.pq[0]
		a := e.armed[it.name]
		if a != nil && it.gen == a.gen {
			return
		}
		heap.Pop(&e.pq)
	}
}

func (e *Engine) rescheduleLocked(a *armed) {
	next, ok := NextDeadline(a.spec, a.rt, e.clk)
	a.next = next
	a.ok = ok
	a.gen++
	if ok {
		heap.Push(&e.pq, &pqItem{name: a.spec.Name, when: next, gen: a.gen})
	}
}

// Arm starts or refreshes a timer. Existing FiredBoot/FiredStartup and last
// unit-active time are kept so a reload does not re-fire one-shot relatives.
func (e *Engine) Arm(spec Spec) {
	if e == nil || spec.Name == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if a, ok := e.armed[spec.Name]; ok {
		a.spec = spec
		e.rescheduleLocked(a)
		e.kickLocked()
		return
	}
	rt := e.store.Load(spec.Name)
	a := &armed{spec: spec, rt: rt}
	e.armed[spec.Name] = a
	e.rescheduleLocked(a)
	e.kickLocked()
}

// Disarm stops a timer. Persistent last-run times stay on disk.
func (e *Engine) Disarm(name string) {
	if e == nil || name == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if a, ok := e.armed[name]; ok {
		a.gen++
		delete(e.armed, name)
	}
	e.kickLocked()
}

// Retain disarms every timer not in keep.
func (e *Engine) Retain(keep map[string]bool) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for name, a := range e.armed {
		if keep[name] {
			continue
		}
		a.gen++
		delete(e.armed, name)
	}
	e.kickLocked()
}

// UnitActive records that the activated unit last became active, for
// OnUnitActiveSec.
func (e *Engine) UnitActive(unit string, when time.Time) {
	if e == nil || unit == "" {
		return
	}
	if when.IsZero() {
		when = e.clk.now()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	changed := false
	for _, a := range e.armed {
		if a.spec.Unit != unit || !a.spec.OnUnitActiveSecSet {
			continue
		}
		a.rt.LastUnitActive = when
		e.rescheduleLocked(a)
		changed = true
	}
	if changed {
		e.kickLocked()
	}
}

// RecordResult stores last successful execution after the activated unit
// start attempt.
func (e *Engine) RecordResult(name string, success bool) {
	if e == nil || name == "" || !success {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	a := e.armed[name]
	if a == nil {
		return
	}
	a.rt.LastSuccess = e.clk.now()
	_ = e.store.Save(name, a.rt)
}

// Snapshot is next/last for status and list-timers.
type Snapshot struct {
	Next time.Time
	Last time.Time
}

// Status returns next and last actual elapse for an armed timer.
func (e *Engine) Status(name string) Snapshot {
	if e == nil {
		return Snapshot{}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	a := e.armed[name]
	if a == nil {
		return Snapshot{}
	}
	out := Snapshot{Last: a.rt.LastActual}
	if a.ok {
		out.Next = a.next
	}
	return out
}

// Armed reports whether name is currently scheduled.
func (e *Engine) Armed(name string) bool {
	if e == nil || name == "" {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.armed[name]
	return ok
}

func (e *Engine) kick() {
	e.mu.Lock()
	e.kickLocked()
	e.mu.Unlock()
}

func (e *Engine) kickLocked() {
	select {
	case e.wakeup <- struct{}{}:
	default:
	}
}

type pqItem struct {
	name string
	when time.Time
	gen  uint64
}

type deadlineHeap []*pqItem

func (h deadlineHeap) Len() int { return len(h) }

func (h deadlineHeap) Less(i, j int) bool {
	if h[i].when.Equal(h[j].when) {
		return h[i].name < h[j].name
	}
	return h[i].when.Before(h[j].when)
}

func (h deadlineHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *deadlineHeap) Push(x any) {
	*h = append(*h, x.(*pqItem))
}

func (h *deadlineHeap) Pop() any {
	old := *h
	n := len(old)
	it := old[n-1]
	old[n-1] = nil
	*h = old[:n-1]
	return it
}
