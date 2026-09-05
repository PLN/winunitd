//go:build windows

package pathwatch

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/sys/windows"
)

// testStatHook, if set, is called on every GetFileAttributes used by Exists
// and resolveExistsWatch. Tests use it to assert ≤1 Stat per filtered wakeup.
var testStatHook func()

// Exists reports whether spec exists on disk.
func Exists(s Spec) (bool, error) {
	if _, err := ParseExists(s.Raw); err != nil {
		return false, err
	}
	_, err := getFileAttributes(s.Raw)
	if err != nil {
		if isMissing(err) {
			return false, nil
		}
		return false, fmt.Errorf("%w: %s: %v", ErrUnwatchable, s.Raw, err)
	}
	return true, nil
}

type existsWatch struct {
	target string
	ch     chan struct{}

	opMu     sync.Mutex // serializes opening, replacement, and cleanup
	pending  []Watch    // replaced watches whose close failed
	mu       sync.Mutex
	closed   bool
	inner    Watch
	watchDir string
	filter   string
}

// OpenExistsWatch watches for spec's existence to change. A missing target
// is OK: the nearest existing ancestor directory is watched (non-recursive)
// so creation of the next component can satisfy PathExists=. Wakeups use
// FILE_NOTIFY_CHANGE_FILE_NAME|DIR_NAME and are filtered to that component's
// basename (sibling noise under the ancestor is ignored). At most one Stat
// runs per filtered wakeup; the caller re-checks Exists to decide AND.
// After each notification the watch re-arms closer to the target when an
// intermediate directory has appeared.
func OpenExistsWatch(s Spec) (Watch, error) {
	if _, err := ParseExists(s.Raw); err != nil {
		return nil, err
	}
	dir, filter, err := resolveExistsWatch(s.Raw)
	if err != nil {
		return nil, err
	}
	inner, err := openExistsDirWatch(dir, filter)
	if err != nil {
		return inner, err
	}
	w := &existsWatch{
		target:   s.Raw,
		ch:       make(chan struct{}, 1),
		inner:    inner,
		watchDir: dir,
		filter:   filter,
	}
	go w.loop()
	return w, nil
}

func openExistsDirWatch(dir, filter string) (Watch, error) {
	if filter == "" {
		return nil, fmt.Errorf("%w: %s", ErrUnwatchable, dir)
	}
	return openDirWatchNotify(dir, filter, existsNotifyFilter)
}

func (w *existsWatch) C() <-chan struct{} { return w.ch }

func (w *existsWatch) Close() error {
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	w.opMu.Lock()
	defer w.opMu.Unlock()
	w.mu.Lock()
	if w.inner != nil {
		w.pending = append(w.pending, w.inner)
		w.inner = nil
	}
	w.mu.Unlock()
	var failed []Watch
	var errs []error
	for _, watch := range w.pending {
		if err := watch.Close(); err != nil {
			failed = append(failed, watch)
			errs = append(errs, err)
		}
	}
	w.pending = failed
	return errors.Join(errs...)
}

func (w *existsWatch) loop() {
	defer close(w.ch)
	for {
		inner := w.getInner()
		if inner == nil {
			return
		}
		_, ok := <-inner.C()
		if w.isClosed() {
			return
		}
		if !ok {
			if !w.rearm("", "") {
				return
			}
			continue
		}
		dir, filter := w.watchSnapshot()
		leaf := isTargetLeafWatch(w.target, dir, filter)
		if leaf {
			// Leaf create/delete/rename: signal once, no Stat here.
			w.signal()
		} else {
			// Ancestor wakeup: one Stat to see whether the full target
			// appeared (e.g. a tree drop). Sibling names never reach here.
			exists, err := Exists(Spec{Raw: w.target})
			if err != nil {
				return
			}
			if exists {
				w.signal()
			}
		}
		if !w.rearmCloser() {
			return
		}
	}
}

func (w *existsWatch) rearm(dir, filter string) bool {
	w.opMu.Lock()
	defer w.opMu.Unlock()
	if w.isClosed() || len(w.pending) != 0 {
		return false
	}
	if dir == "" {
		var err error
		dir, filter, err = resolveExistsWatch(w.target)
		if err != nil {
			return false
		}
	}
	next, err := openExistsDirWatch(dir, filter)
	if err != nil {
		if next != nil {
			w.pending = append(w.pending, next)
		}
		return false
	}
	return w.replaceInner(next, dir, filter)
}

// rearmCloser opens the next existing intermediate directory toward the
// target without walking/statting the full path. CreateFile on a missing
// next component is a no-op stay.
func (w *existsWatch) rearmCloser() bool {
	w.opMu.Lock()
	defer w.opMu.Unlock()
	if w.isClosed() || len(w.pending) != 0 {
		return false
	}
	dir, filter := w.watchSnapshot()
	if isTargetLeafWatch(w.target, dir, filter) {
		return true
	}
	for {
		nextDir, nextFilter, ok := nextExistsStep(w.target, dir, filter)
		if !ok {
			return true
		}
		next, err := openExistsDirWatch(nextDir, nextFilter)
		if err != nil {
			if next != nil {
				w.pending = append(w.pending, next)
				return false
			}
			return true
		}
		if !w.replaceInner(next, nextDir, nextFilter) {
			return false
		}
		dir, filter = nextDir, nextFilter
	}
}

func (w *existsWatch) signal() {
	if w.isClosed() {
		return
	}
	select {
	case w.ch <- struct{}{}:
	default:
	}
}

func (w *existsWatch) isClosed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}

func (w *existsWatch) getInner() Watch {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	return w.inner
}

func (w *existsWatch) watchSnapshot() (dir, filter string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.watchDir, w.filter
}

// replaceInner requires opMu. Every successfully opened watch remains owned,
// including one opened while Close was waiting for this operation.
func (w *existsWatch) replaceInner(inner Watch, dir, filter string) bool {
	w.mu.Lock()
	if w.closed {
		w.pending = append(w.pending, inner)
		w.mu.Unlock()
		return false
	}
	old := w.inner
	w.inner = inner
	w.watchDir = dir
	w.filter = filter
	w.mu.Unlock()
	if old != nil {
		if err := old.Close(); err != nil {
			w.pending = append(w.pending, old)
			return false
		}
	}
	return true
}

func resolveExistsWatch(raw string) (dir, filter string, err error) {
	target := strings.TrimRight(strings.TrimSpace(raw), `/\`)
	if target == "" {
		return "", "", fmt.Errorf("%w: %s", ErrUnwatchable, raw)
	}
	if _, e := getFileAttributes(raw); e == nil {
		parent, name := SplitDirName(raw)
		if parent == "" || name == "" {
			return "", "", fmt.Errorf("%w: %s", ErrUnwatchable, raw)
		}
		return parent, name, nil
	} else if !isMissing(e) {
		return "", "", fmt.Errorf("%w: %s: %v", ErrUnwatchable, raw, e)
	}

	p := target
	for {
		parent, name := SplitDirName(p)
		if parent == "" || name == "" {
			return "", "", fmt.Errorf("%w: %s", ErrUnwatchable, raw)
		}
		attrs, e := getFileAttributes(parent)
		if e == nil {
			if attrs&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
				return "", "", fmt.Errorf("%w: %s", ErrUnwatchable, raw)
			}
			return parent, name, nil
		}
		if !isMissing(e) {
			return "", "", fmt.Errorf("%w: %s: %v", ErrUnwatchable, raw, e)
		}
		next := strings.TrimRight(parent, `/\`)
		if next == "" || next == p {
			return "", "", fmt.Errorf("%w: %s", ErrUnwatchable, raw)
		}
		p = next
	}
}

func getFileAttributes(raw string) (uint32, error) {
	if h := testStatHook; h != nil {
		h()
	}
	p, err := windows.UTF16PtrFromString(raw)
	if err != nil {
		return 0, err
	}
	return windows.GetFileAttributes(p)
}
