package timers

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func intentFixture(t *testing.T) (*Store, Spec, time.Time) {
	t.Helper()
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cal, err := ParseCalendar("daily")
	if err != nil {
		t.Fatal(err)
	}
	return store, Spec{Name: "work.timer", Unit: "work.service", OnCalendar: []Calendar{cal}, Persistent: true}, time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
}

func awaitIntent(t *testing.T, e *Engine, name, result string) Snapshot {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s := e.Status(name)
		if s.Activation.Result == result {
			return s
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("intent never became %s: %+v", result, e.Status(name))
	return Snapshot{}
}

func TestPendingTimerIntentRecoversOnceAndCoalescesMissedCalendar(t *testing.T) {
	store, spec, now := intentFixture(t)
	intent := Activation{ID: "retained-intent", Unit: spec.Unit, Result: "pending", Scheduled: now.Add(-72 * time.Hour), Actual: now.Add(-72 * time.Hour)}
	if err := store.Save(spec.Name, Runtime{Activation: intent, LastScheduled: intent.Scheduled, LastActual: intent.Actual}); err != nil {
		t.Fatal(err)
	}
	recovered := make(chan Fire, 4)
	var e *Engine
	e = NewEngine(NewFake(now).Clock(), store, func(f Fire) { e.RecordResult(f, true); recovered <- f })
	defer e.Stop()
	e.Arm(spec)
	select {
	case f := <-recovered:
		if f.ActivationID != intent.ID {
			t.Fatal("recovery replaced activation identity")
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("pending activation not retried: %+v", e.Status(spec.Name))
	}
	s := awaitIntent(t, e, spec.Name, "success")
	if !s.Last.Equal(now) {
		t.Fatal("recovery did not coalesce missed occurrences")
	}
	e.Stop()
	got, err := store.LoadChecked(spec.Name)
	if err != nil || got.Activation.Result != "success" || got.Activation.ID != intent.ID {
		t.Fatalf("result not durable: %+v %v", got, err)
	}
	// A second boot after the result must await the next scheduled occurrence.
	again := NewEngine(NewFake(now).Clock(), store, func(f Fire) { recovered <- f })
	defer again.Stop()
	again.Arm(spec)
	waitStorageReady(t, again, spec.Name)
	select {
	case <-recovered:
		t.Fatal("completed intent replayed")
	case <-time.After(30 * time.Millisecond):
	}
}

func TestTimerFailedResultWriteRetainsRetryableIntent(t *testing.T) {
	store, spec, now := intentFixture(t)
	if err := store.Save(spec.Name, Runtime{LastActual: now.Add(-48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	real := &Store{dir: store.dir}
	var writes atomic.Int32
	store.save = func(name string, rt Runtime) error {
		if writes.Add(1) == 2 {
			return errors.New("result write failed")
		}
		return real.Save(name, rt)
	}
	calls := make(chan Fire, 4)
	var e *Engine
	e = NewEngine(NewFake(now).Clock(), store, func(f Fire) {
		// The pending record must already exist before the activation side effect.
		durable, err := real.LoadChecked(spec.Name)
		if err != nil || durable.Activation.Result != "pending" || durable.Activation.ID != f.ActivationID {
			t.Errorf("dispatch without durable intent: %+v %v", durable, err)
		}
		e.RecordResult(f, true)
		calls <- f
	})
	defer e.Stop()
	e.Arm(spec)
	var first Fire
	select {
	case first = <-calls:
	case <-time.After(time.Second):
		t.Fatal("initial activation missing")
	}
	if e.Status(spec.Name).StorageError == "" {
		t.Fatal("result write failure hidden")
	}
	if e.Status(spec.Name).Activation.Result != "pending" {
		t.Fatal("failed write published a durable result")
	}
	e.Stop()
	disk, err := real.LoadChecked(spec.Name)
	if err != nil || disk.Activation.Result != "pending" {
		t.Fatalf("failed result write lost pending intent: %+v %v", disk, err)
	}
	var second *Engine
	second = NewEngine(NewFake(now.Add(time.Hour)).Clock(), real, func(f Fire) { second.RecordResult(f, false); calls <- f })
	defer second.Stop()
	second.Arm(spec)
	select {
	case f := <-calls:
		if f.ActivationID != first.ActivationID {
			t.Fatal("retry did not retain intent")
		}
	case <-time.After(time.Second):
		t.Fatal("interrupted result not retried")
	}
	second.Stop()
	disk, err = real.LoadChecked(spec.Name)
	if err != nil || disk.Activation.Result != "failed" || !disk.LastSuccess.IsZero() {
		t.Fatalf("failed activation result not durable: %+v %v", disk, err)
	}
}

func TestTimerResultRequiresActivationIdentity(t *testing.T) {
	var writes atomic.Int32
	clock := NewFake(time.Time{})
	a := &armed{token: 1, spec: Spec{Name: "work.timer"}, rt: Runtime{Activation: Activation{ID: "current", Result: "pending"}}}
	e := &Engine{clk: clock.Clock(), running: true, armed: map[string]*armed{"work.timer": a}, store: &Store{save: func(string, Runtime) error { writes.Add(1); return nil }}}
	e.RecordResult(Fire{Name: "work.timer", Token: 1, ActivationID: "old"}, true)
	if writes.Load() != 0 || a.rt.Activation.Result != "pending" {
		t.Fatal("stale activation result changed current intent")
	}
	e.RecordResult(Fire{Name: "work.timer", Token: 1, ActivationID: "current"}, true)
	if writes.Load() != 1 || a.rt.Activation.Result != "success" {
		t.Fatal("current activation result not committed")
	}
}

func TestPendingTimerIntentRejectsRetargeting(t *testing.T) {
	store, spec, now := intentFixture(t)
	a := Activation{ID: "pending", Unit: "old.service", Result: "pending", Scheduled: now, Actual: now}
	if err := store.Save(spec.Name, Runtime{Activation: a}); err != nil {
		t.Fatal(err)
	}
	fired := make(chan string, 1)
	e := NewEngine(NewFake(now).Clock(), store, func(Fire) { fired <- "fired" })
	defer e.Stop()
	e.Arm(spec)
	until := time.Now().Add(time.Second)
	for e.Status(spec.Name).StorageState == "loading" && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if s := e.Status(spec.Name); s.StorageState != "failed" || !strings.Contains(s.StorageError, "target") {
		t.Fatalf("retargeted pending intent: %+v", s)
	}
	select {
	case <-fired:
		t.Fatal("changed target activated")
	default:
	}
}

func TestTimerIntentStateMigrationAndValidation(t *testing.T) {
	store, spec, now := intentFixture(t)
	path := filepath.Join(store.dir, spec.Name+".json")
	for _, version := range []string{"", `"version":1,`} {
		body := `{` + version + `"lastActual":"` + formatStamp(now) + `"}`
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		rt, err := store.LoadChecked(spec.Name)
		if err != nil || !rt.LastActual.Equal(now) || rt.Activation.ID != "" {
			t.Fatalf("legacy state: %+v %v", rt, err)
		}
		if err := store.Save(spec.Name, rt); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(data), `"version":2`) {
			t.Fatal("legacy state not upgraded")
		}
	}
	for _, body := range []string{`{"version":1,"activation":{}}`, `{"version":2,"activation":{"id":"x","result":"pending"}}`, `{"version":2,"activation":{"id":"x","result":"other"}}`} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LoadChecked(spec.Name); err == nil {
			t.Fatal("invalid intent accepted")
		}
	}
}
