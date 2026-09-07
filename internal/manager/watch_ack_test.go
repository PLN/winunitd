package manager

import (
	"testing"
	"time"
)

// runWatch asks for C again only after its synchronous activation callback
// returns. Observing that next receive proves the event has been processed.
type acknowledgedWatch struct {
	watchIO
	waiting chan struct{}
}

func (w *acknowledgedWatch) C() <-chan struct{} {
	w.waiting <- struct{}{}
	return w.watchIO.C()
}

func waitWatchCycle(t *testing.T, waiting <-chan struct{}) {
	t.Helper()
	select {
	case <-waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not reach its next receive")
	}
}
