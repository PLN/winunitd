package manager

import (
	"context"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/core"
)

type blockedDiagnosticWriter struct {
	entered, release chan struct{}
	once             sync.Once
}

func (w *blockedDiagnosticWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return len(p), nil
}

func TestRejectedTransitionDoesNotWaitForConsoleLogging(t *testing.T) {
	m := managerWith(t, &fakeLauncher{}, map[string]string{
		"work.target":  "[Unit]\nDescription=Owned target\n",
		"other.target": "[Unit]\nDescription=Independent target\n",
	})
	if _, err := m.Start(context.Background(), "work.target"); err != nil {
		t.Fatal(err)
	}
	w := &blockedDiagnosticWriter{entered: make(chan struct{}), release: make(chan struct{})}
	previous := log.Writer()
	log.SetOutput(w)
	consoleDone := make(chan struct{})
	go func() { log.Print("held diagnostic sink"); close(consoleDone) }()
	defer func() {
		close(w.release)
		<-consoleDone
		log.SetOutput(previous)
	}()
	awaitNativeWork(t, w.entered)
	decision := make(chan struct{})
	go func() {
		m.mu.Lock()
		m.units["work.target"].step(core.EventStopFinished)
		m.mu.Unlock()
		close(decision)
	}()
	select {
	case <-decision:
	case <-time.After(time.Second):
		t.Fatal("rejected transition held the coordinator behind console logging")
	}
	snapshot, err := m.Snapshot()
	if err != nil || snapshot.Machine.UnitsActive != 1 {
		t.Fatal("rejection changed accepted lifecycle state", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := m.Start(ctx, "other.target"); err != nil {
		t.Fatal("independent start blocked behind diagnostic output", err)
	}
	if _, err := m.StopContext(ctx, "work.target"); err != nil {
		t.Fatal("stop blocked behind diagnostic output", err)
	}
	if err := m.CloseContext(ctx); err != nil {
		t.Fatal("diagnostic persistence did not join manager close", err)
	}
	entries, err := m.journal.Read("work.target")
	if err != nil || len(entries) != 1 || entries[0].Stream != "daemon" || !strings.Contains(entries[0].Message, "illegal transition") {
		t.Fatalf("rejection diagnostic was not retained: entries=%d error=%v", len(entries), err)
	}
}
