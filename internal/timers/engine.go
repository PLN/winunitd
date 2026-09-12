package timers

import (
	"container/heap"
	"crypto/rand"
	"fmt"
	"sync"
	"time"
)

// maxWait caps how long the engine sleeps before recomputing deadlines
// so calendar timers recover from clock/DST changes (DESIGN.md §18).
const maxWait = 30 * time.Second

// Callback work is bounded separately from the number of armed timers.
const maxTimerCallbacks = 32
const MaxArmedTimers = 1024
const MaxCalendarExpressions = 64
const admissionRetryDelay = 250 * time.Millisecond

// Fire captures the source arm and target when a deadline is consumed.
type Fire struct {
	ActivationID string
	Name         string
	Unit         string
	Token        uint64 // identity of the arm that produced this event
}

// FireFunc runs asynchronously without the engine lock held.
type FireFunc func(Fire)

// Engine is an internal timer scheduler (not Task Scheduler).
type Engine struct {
	clk   Clock
	store *Store
	fire  FireFunc

	mu             sync.Mutex
	storageIO      sync.Mutex // persistence only; never held by a decision handler
	storageWork    sync.WaitGroup
	scheduleWork   sync.WaitGroup
	scheduling     bool
	scheduleCursor string
	loading        bool // one loader drains pending arms within the arm budget
	namespace      string
	nextActivation uint64
	armed          map[string]*armed
	pq             deadlineHeap
	wakeup         chan struct{}
	stop           chan struct{}
	stopped        chan struct{}
	running        bool
	fires          sync.WaitGroup
	clockGen       uint64 // incremented on ClockChanged; armed.schedGen tracks it
	activeFires    int
	nextArm        uint64 // unique callback identity across refresh/disarm/rearm
	nextGen        uint64 // unique schedule identity across disarm/rearm of the same name

	// onNextDeadline is a test hook, called outside decision locks by the
	// reserved calendar worker. Configure before submitting calendar work.
	onNextDeadline func()
}

type pendingRetry struct {
	event Fire
	after time.Duration
}

type armed struct {
	item         *pqItem
	planning     bool
	loading      bool
	storageError string
	firing       bool
	retry        *pendingRetry
	token        uint64
	spec         Spec
	rt           Runtime
	next         time.Time
	ok           bool
	gen          uint64
	schedGen     uint64 // e.clockGen at last rescheduleLocked
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
		namespace: rand.Text(),
		clk:       clk,
		store:     store,
		fire:      fire,
		armed:     make(map[string]*armed),
		wakeup:    make(chan struct{}, 1),
		stop:      make(chan struct{}),
		stopped:   make(chan struct{}),
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
		<-e.stopped
		e.fires.Wait()
		e.storageWork.Wait()
		e.scheduleWork.Wait()
		return
	}
	e.running = false
	e.mu.Unlock()
	close(e.stop)
	<-e.stopped
	e.fires.Wait()
	e.storageWork.Wait()
	e.scheduleWork.Wait()
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
	defer e.mu.Unlock()
	if !e.running {
		return
	}
	e.clockGen++
	e.fireDueLocked(e.clk.now())
	// Capacity-limited or newly due occurrences retain their accepted deadline.
	// A fresh calendar search from the new wall time would silently skip them.
	now := e.clk.now()
	for _, a := range e.armed {
		if !wallSensitive(a.spec) {
			continue
		}
		if a.item != nil && e.itemDueLocked(a, a.item, now) {
			a.schedGen = e.clockGen
			continue
		}
		e.rescheduleLocked(a)
	}
	e.kickLocked()
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
		if a == nil || it.gen != a.gen || a.firing || e.activeFires >= maxTimerCallbacks {
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
	e.mu.Lock()
	defer e.mu.Unlock()
	e.fireDueLocked(e.clk.now())
}

// Dequeue and acceptance are one decision. Rescheduling cannot invalidate a
// popped occurrence before its activation is recorded. Workers run after unlock.
func (e *Engine) fireDueLocked(now time.Time) {
	for {
		due, ok := e.popDueLocked(now)
		if !ok {
			return
		}
		e.consumeLocked(due, now)
	}
}

// dueTimer retains both the armed instance and its schedule generation across
// explicit delayed-result tests. Production dequeue/consumption is atomic.
type dueTimer struct {
	instance   *armed
	generation uint64
	scheduled  time.Time
}

func (e *Engine) popDue(now time.Time) (due dueTimer, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.popDueLocked(now)
}

func (e *Engine) popDueLocked(now time.Time) (due dueTimer, ok bool) {
	e.dropStaleLocked()
	best := -1
	for i, it := range e.pq {
		a := e.armed[it.name]
		if a == nil || it.gen != a.gen || a.firing || e.activeFires >= maxTimerCallbacks {
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
		return dueTimer{}, false
	}
	it := heap.Remove(&e.pq, best).(*pqItem)
	if a := e.armed[it.name]; a != nil && a.item == it {
		a.item = nil
	}
	return dueTimer{instance: e.armed[it.name], generation: it.gen, scheduled: it.when}, true
}

func (e *Engine) consume(due dueTimer, actual time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.consumeLocked(due, actual)
}

func (e *Engine) consumeLocked(due dueTimer, actual time.Time) {
	if !e.running {
		return
	}
	a := due.instance
	if a == nil || e.armed[a.spec.Name] != a || a.gen != due.generation {
		return
	}
	if a.firing || e.activeFires >= maxTimerCallbacks {
		// Preserve the popped occurrence, even for non-persistent calendars.
		e.installDeadlineLocked(a, due.scheduled, due.generation)
		return
	}
	name := a.spec.Name
	event := Fire{Name: name, Unit: a.spec.Unit, Token: a.token}
	if a.retry != nil {
		event = a.retry.event
		a.retry = nil
		if event.ActivationID != "" {
			a.rt.LastActual = actual
			a.rt.Activation.Actual = actual
		}
	} else {
		MarkFired(a.spec, &a.rt, e.clk, due.scheduled, actual)
		if a.spec.Persistent && len(a.spec.OnCalendar) > 0 {
			e.nextActivation++
			event.ActivationID = fmt.Sprintf("%s-%d", e.namespace, e.nextActivation)
			a.rt.Activation = Activation{ID: event.ActivationID, Unit: event.Unit, Result: "pending", Scheduled: due.scheduled, Actual: actual}
		}
	}
	fire := e.fire
	rt := a.rt
	e.fires.Add(1)
	e.activeFires++
	a.firing = true

	go func() {
		defer e.fires.Done()
		if e.persist(event, rt) && fire != nil && e.Current(event.Name, event.Token) {
			fire(event)
		}
		e.mu.Lock()
		e.activeFires--
		if current := e.armed[name]; current == a && a.token == event.Token {
			a.firing = false
			if a.storageError == "" && a.rt.Activation.Result == "pending" && a.retry == nil {
				a.storageError = "activation callback did not record an outcome; re-arm to recover pending intent"
			}
			if e.running {
				e.rescheduleLocked(a)
			}
		}
		e.mu.Unlock()
		e.kick()
	}()

	if a2 := e.armed[name]; a2 == a && a.token == event.Token {
		e.rescheduleLocked(a)
	}
	e.kickLocked()
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
	if a.retry != nil {
		return a.retry.after - e.clk.sinceStart()
	}
	if wallSensitive(a.spec) {
		return it.when.Sub(now)
	}
	return e.monotonicWaitLocked(a.spec, a.rt)
}

func (e *Engine) itemDueLocked(a *armed, it *pqItem, now time.Time) bool {
	if a.retry != nil {
		return e.clk.sinceStart() >= a.retry.after
	}
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
	e.removeDeadlineLocked(a)
	e.nextGen++
	a.gen = e.nextGen
	a.planning = false
	a.ok = false
	a.next = time.Time{}
	if a.loading || a.storageError != "" {
		return
	}
	if a.retry == nil && len(a.spec.OnCalendar) > 0 {
		a.planning = true
		if e.running && !e.scheduling {
			e.scheduling = true
			e.scheduleWork.Add(1)
			go e.planCalendars()
		}
		return
	}
	var next time.Time
	var ok bool
	if a.retry != nil {
		next, ok = e.clk.now().Add(max(0, a.retry.after-e.clk.sinceStart())), true
	} else {
		next, ok = NextDeadline(a.spec, a.rt, e.clk)
	}
	a.next = next
	a.ok = ok
	a.schedGen = e.clockGen
	if ok {
		e.installDeadlineLocked(a, next, a.gen)
	}
}

func (e *Engine) removeDeadlineLocked(a *armed) {
	if it := a.item; it != nil {
		if it.index >= 0 && it.index < len(e.pq) && e.pq[it.index] == it {
			heap.Remove(&e.pq, it.index)
		}
		a.item = nil
	}
}

func (e *Engine) installDeadlineLocked(a *armed, when time.Time, gen uint64) {
	e.removeDeadlineLocked(a)
	it := &pqItem{name: a.spec.Name, when: when, gen: gen, index: -1}
	a.item = it
	heap.Push(&e.pq, it)
}

// Arm starts or refreshes a timer. Existing FiredBoot/FiredStartup and last
// unit-active time are kept so refresh does not re-fire one-shot relatives.
// Each arm receives a new callback identity, returned to the caller.
func (e *Engine) Arm(spec Spec) uint64 {
	if e == nil || spec.Name == "" || len(spec.OnCalendar) > MaxCalendarExpressions {
		return 0
	}
	spec.OnCalendar = append([]Calendar(nil), spec.OnCalendar...)
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running {
		return 0
	}
	if e.armed[spec.Name] == nil && len(e.armed) >= MaxArmedTimers {
		return 0
	}
	e.nextArm++
	if a, ok := e.armed[spec.Name]; ok {
		a.token = e.nextArm
		a.firing = false
		a.retry = nil
		a.spec = spec
		if a.storageError != "" || a.rt.Activation.Result == "pending" {
			a.storageError = ""
			a.loading = true
			if !e.loading {
				e.loading = true
				e.storageWork.Add(1)
				go e.loadPending()
			}
		}
		e.rescheduleLocked(a)
		e.kickLocked()
		return a.token
	}
	a := &armed{spec: spec, token: e.nextArm}
	e.armed[spec.Name] = a
	if e.store != nil && (e.store.dir != "" || e.store.load != nil) {
		a.loading = true
		if !e.loading {
			e.loading = true
			e.storageWork.Add(1)
			go e.loadPending()
		}
	}
	e.rescheduleLocked(a)
	e.kickLocked()
	return a.token
}

// Disarm stops a timer. Persistent last-run times stay on disk.
func (e *Engine) Disarm(name string) {
	if e == nil || name == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if a, ok := e.armed[name]; ok {
		e.removeDeadlineLocked(a)
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
		e.removeDeadlineLocked(a)
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
func (e *Engine) RecordResult(event Fire, success bool) {
	name := event.Name
	if e == nil || name == "" {
		return
	}
	e.mu.Lock()
	a := e.armed[name]
	if !e.running || a == nil || a.loading || a.storageError != "" || a.token != event.Token {
		e.mu.Unlock()
		return
	}
	rt := a.rt
	if event.ActivationID != "" {
		if a.rt.Activation.ID != event.ActivationID || a.rt.Activation.Result != "pending" {
			e.mu.Unlock()
			return
		}
		rt.Activation.Result = "failed"
		if success {
			rt.Activation.Result = "success"
		}
	} else if a.rt.Activation.Result == "pending" || !success {
		e.mu.Unlock()
		return
	}
	if success {
		rt.LastSuccess = e.clk.now()
	}
	e.storageWork.Add(1)
	e.mu.Unlock()
	defer e.storageWork.Done()
	if e.persist(event, rt) {
		e.mu.Lock()
		if e.running && e.armed[name] == a && a.token == event.Token && (event.ActivationID == "" || a.rt.Activation.ID == event.ActivationID) {
			a.rt.LastSuccess = rt.LastSuccess
			a.rt.Activation = rt.Activation
			a.retry = nil
		}
		e.mu.Unlock()
	}
}

// Snapshot is next/last for status and list-timers.
type Snapshot struct {
	ScheduleState  string
	Activation     Activation
	StorageState   string
	StorageError   string
	ConfigRevision string
	Unit           string

	Next time.Time
	Last time.Time
}

// Status copies accepted next/last state without performing calendar searches.
// Next is absent while storage or calendar planning is pending, and otherwise
// matches the indexed heap deadline.
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
	out := Snapshot{Activation: a.rt.Activation, ConfigRevision: a.spec.ConfigRevision, Unit: a.spec.Unit, Last: a.rt.LastActual, StorageState: "ready", ScheduleState: "ready", StorageError: a.storageError}
	if a.loading || a.storageError != "" {
		out.StorageState, out.ScheduleState = "loading", "waiting"
		if a.storageError != "" {
			out.StorageState, out.ScheduleState = "failed", "failed"
		}
		return out
	}
	if a.planning || (wallSensitive(a.spec) && a.schedGen != e.clockGen) {
		out.ScheduleState = "planning"
		return out
	}
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
	index int
	name  string
	when  time.Time
	gen   uint64
}

type deadlineHeap []*pqItem

func (h deadlineHeap) Len() int { return len(h) }

func (h deadlineHeap) Less(i, j int) bool {
	if h[i].when.Equal(h[j].when) {
		return h[i].name < h[j].name
	}
	return h[i].when.Before(h[j].when)
}

func (h deadlineHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *deadlineHeap) Push(x any) {
	it := x.(*pqItem)
	it.index = len(*h)
	*h = append(*h, it)
}

func (h *deadlineHeap) Pop() any {
	old := *h
	n := len(old)
	it := old[n-1]
	it.index = -1
	old[n-1] = nil
	*h = old[:n-1]
	return it
}

// Current reports whether an event still belongs to the current arm.
func (e *Engine) Current(name string, token uint64) bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	a := e.armed[name]
	return e.running && a != nil && !a.loading && a.storageError == "" && a.token == token
}

// Retry retains an activation rejected before manager admission. Persistent
// calendar retries keep their durable intent identity; other retries retain
// their in-memory arm identity. Neither records a successful execution.
func (e *Engine) Retry(event Fire) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	a := e.armed[event.Name]
	if !e.running || a == nil || a.token != event.Token {
		return
	}
	a.retry = &pendingRetry{event: event, after: e.clk.sinceStart() + admissionRetryDelay}
	e.rescheduleLocked(a)
	e.kickLocked()
}
