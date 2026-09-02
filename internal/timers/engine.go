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
	fires   sync.WaitGroup

	// onStatusDeadline is a test hook (nil in production). It fires after
	// e.mu is released and before NextDeadline. Status must not hold the
	// engine lock across calendar search.
	onStatusDeadline func()
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
	if clk.NewTimer == nil {
		clk.NewTimer = stdNewTimer
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

// Stop ends the scheduler goroutine and waits for in-flight fire
// callbacks. It is idempotent.
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
	e.fires.Wait()
}

// ClockChanged recomputes wall-clock (OnCalendar / OnUnitActiveSec)
// deadlines and wakes the loop so missed calendar events fire promptly
// (DESIGN.md §18). Monotonic OnBootSec / OnStartupSec heap entries are
// not rewritten. The 30s poll remains as a fallback.
func (e *Engine) ClockChanged() {
	if e == nil {
		return
	}
	e.mu.Lock()
	if !e.running {
		e.mu.Unlock()
		return
	}
	e.mu.Unlock()
	e.fireDue()
	e.recalcWall()
	e.kick()
}

func (e *Engine) loop() {
	defer close(e.stopped)
	var timer Timer
	stopTimer := func() {
		if timer == nil {
			return
		}
		if !timer.Stop() {
			select {
			case <-timer.C():
			default:
			}
		}
		timer = nil
	}
	defer stopTimer()
	for {
		wait, useTimer := e.waitDuration()
		stopTimer()
		var timerC <-chan time.Time
		if useTimer {
			timer = e.clk.Timer(wait)
			timerC = timer.C()
		}
		select {
		case <-e.stop:
			return
		case <-e.wakeup:
			continue
		case <-e.clk.changed():
			e.ClockChanged()
			continue
		case <-timerC:
			timer = nil
			e.fireDue()
		}
	}
}

func (e *Engine) waitDuration() (time.Duration, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.clk.now()
	e.dropStaleLocked()
	found := false
	d := maxWait
	for _, it := range e.pq {
		a := e.armed[it.name]
		if a == nil || it.gen != a.gen {
			continue
		}
		w := e.itemWaitLocked(a, it, now)
		if w < 0 {
			w = 0
		}
		if !found || w < d {
			d = w
			found = true
		}
	}
	if !found {
		if e.clk.Changed == nil {
			return maxWait, true
		}
		return 0, false
	}
	if e.clk.Changed == nil && d > maxWait {
		d = maxWait
	}
	return d, true
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

func (e *Engine) recalcWall() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, a := range e.armed {
		if wallSensitive(a.spec) {
			e.rescheduleLocked(a)
		}
	}
}

func (e *Engine) popDue(now time.Time) (name string, scheduled time.Time, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.dropStaleLocked()
	best := -1
	for i, it := range e.pq {
		a := e.armed[it.name]
		if a == nil || it.gen != a.gen {
			continue
		}
		if !e.itemDueLocked(a, it, now) {
			continue
		}
		if best < 0 || e.pq.Less(i, best) {
			best = i
		}
	}
	if best < 0 {
		return "", time.Time{}, false
	}
	it := heap.Remove(&e.pq, best).(*pqItem)
	return it.name, it.when, true
}

func (e *Engine) consume(name string, scheduled, actual time.Time) {
	e.mu.Lock()
	if !e.running {
		e.mu.Unlock()
		return
	}
	a := e.armed[name]
	if a == nil {
		e.mu.Unlock()
		return
	}
	MarkFired(a.spec, &a.rt, e.clk, scheduled, actual)
	_ = e.store.Save(name, a.rt)
	fire := e.fire
	if fire != nil {
		e.fires.Add(1)
	}
	e.mu.Unlock()

	if fire != nil {
		go func() {
			defer e.fires.Done()
			fire(name)
		}()
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

func wallSensitive(spec Spec) bool {
	return len(spec.OnCalendar) > 0 || spec.OnUnitActiveSecSet
}

func (e *Engine) itemWaitLocked(a *armed, it *pqItem, now time.Time) time.Duration {
	if wallSensitive(a.spec) {
		return it.when.Sub(now)
	}
	return e.monotonicWaitLocked(a.spec, a.rt)
}

func (e *Engine) itemDueLocked(a *armed, it *pqItem, now time.Time) bool {
	if wallSensitive(a.spec) {
		return !it.when.After(now)
	}
	return e.monotonicDueLocked(a.spec, a.rt)
}

func (e *Engine) monotonicDueLocked(spec Spec, rt Runtime) bool {
	if spec.OnBootSecSet && !rt.FiredBoot && e.clk.sinceBoot() >= spec.OnBootSec {
		return true
	}
	if spec.OnStartupSecSet && !rt.FiredStartup && e.clk.sinceStart() >= spec.OnStartupSec {
		return true
	}
	return false
}

func (e *Engine) monotonicWaitLocked(spec Spec, rt Runtime) time.Duration {
	var d time.Duration
	found := false
	consider := func(rem time.Duration) {
		if rem < 0 {
			rem = 0
		}
		if !found || rem < d {
			d = rem
			found = true
		}
	}
	if spec.OnBootSecSet && !rt.FiredBoot {
		consider(spec.OnBootSec - e.clk.sinceBoot())
	}
	if spec.OnStartupSecSet && !rt.FiredStartup {
		consider(spec.OnStartupSec - e.clk.sinceStart())
	}
	if !found {
		return 0
	}
	return d
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
// Next is recomputed from the current clock so list-timers is not stale
// across a wall jump (DESIGN.md §18). Spec/runtime are copied under e.mu;
// NextDeadline runs unlocked so a calendar search does not nest e.mu
// inside the manager lock or stall the scheduler loop.
func (e *Engine) Status(name string) Snapshot {
	if e == nil {
		return Snapshot{}
	}
	e.mu.Lock()
	a := e.armed[name]
	if a == nil {
		e.mu.Unlock()
		return Snapshot{}
	}
	spec := a.spec
	rt := a.rt
	clk := e.clk
	last := a.rt.LastActual
	e.mu.Unlock()
	if e.onStatusDeadline != nil {
		e.onStatusDeadline()
	}
	out := Snapshot{Last: last}
	if next, ok := NextDeadline(spec, rt, clk); ok {
		out.Next = next
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
