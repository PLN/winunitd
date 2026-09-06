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

func TestExistsRearmRetainsPartialOpen(t *testing.T) {
	for _, closer := range []bool{false, true} {
		name := "replacement"
		if closer {
			name = "closer-ancestor"
		}
		t.Run(name, func(t *testing.T) {
			failure := errors.New("injected partial open")
			old := &retryExistsWatch{}
			partial := &retryExistsWatch{err: failure}
			opens := 0
			w := &existsWatch{
				target: `C:\lab\next\target`, watchDir: `C:\lab`, filter: "next", inner: old,
				open: func(string, string) (Watch, error) {
					opens++
					return partial, failure
				},
			}
			rearm := func() bool {
				if closer {
					return w.rearmCloser()
				}
				return w.rearm(`C:\lab\next`, "target")
			}
			if rearm() || opens != 1 {
				t.Fatal("partial open did not stop rearming")
			}
			if rearm() || opens != 1 {
				t.Fatal("unresolved resource allowed another open")
			}
			if err := w.Close(); !errors.Is(err, failure) {
				t.Fatalf("partial cleanup error lost: %v", err)
			}
			if old.calls != 1 || partial.calls != 1 {
				t.Fatal("close did not attempt both owned resources")
			}
			partial.err = nil
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			if old.calls != 1 || partial.calls != 2 || len(w.pending) != 0 {
				t.Fatal("retry repeated completed cleanup or lost partial resource")
			}
		})
	}
}
