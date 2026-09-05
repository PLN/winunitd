//go:build windows

package pathwatch

import (
	"errors"
	"testing"
)

type retryExistsWatch struct {
	ch    chan struct{}
	err   error
	calls int
}

func (w *retryExistsWatch) C() <-chan struct{} { return w.ch }
func (w *retryExistsWatch) Close() error       { w.calls++; return w.err }

func TestExistsReplacementRetainsFailedCleanup(t *testing.T) {
	failure := errors.New("injected close failure")
	old := &retryExistsWatch{err: failure}
	next := &retryExistsWatch{}
	w := &existsWatch{inner: old}
	w.opMu.Lock()
	ok := w.replaceInner(next, "test", "target")
	w.opMu.Unlock()
	if ok || len(w.pending) != 1 || w.inner != next {
		t.Fatal("failed replacement cleanup lost ownership or allowed rearming")
	}
	if err := w.Close(); !errors.Is(err, failure) {
		t.Fatalf("Close = %v", err)
	}
	if next.calls != 1 || len(w.pending) != 1 {
		t.Fatal("close did not retain only failed cleanup")
	}
	old.err = nil
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if len(w.pending) != 0 || old.calls != 3 || next.calls != 1 {
		t.Fatal("retry did not complete retained cleanup")
	}
}

func TestExistsCloseOwnsLateOpen(t *testing.T) {
	next := &retryExistsWatch{}
	w := &existsWatch{closed: true}
	w.opMu.Lock()
	ok := w.replaceInner(next, "test", "target")
	w.opMu.Unlock()
	if ok || w.inner != nil || len(w.pending) != 1 {
		t.Fatal("late open escaped ownership")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if next.calls != 1 || len(w.pending) != 0 {
		t.Fatal("late open was not closed")
	}
}
