//go:build windows

package pathwatch

import (
	"fmt"
	"strings"
	"sync"

	"golang.org/x/sys/windows"
)

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

	mu       sync.Mutex
	closed   bool
	inner    Watch
	watchDir string
}

// OpenExistsWatch watches for spec's existence to change. A missing target
// is OK: the nearest existing ancestor directory is watched (non-recursive)
// so creation of the next component can satisfy PathExists=. Notifications
// are any change in that ancestor (no basename filter): the caller re-checks
// Exists. After each notification the watch re-arms closer to the target
// when an intermediate directory has appeared.
func OpenExistsWatch(s Spec) (Watch, error) {
	if _, err := ParseExists(s.Raw); err != nil {
		return nil, err
	}
	dir, _, err := resolveExistsWatch(s.Raw)
	if err != nil {
		return nil, err
	}
	inner, err := openDirWatch(dir, "")
	if err != nil {
		return nil, err
	}
	w := &existsWatch{
		target:   s.Raw,
		ch:       make(chan struct{}, 1),
		inner:    inner,
		watchDir: dir,
	}
	go w.loop()
	return w, nil
}

func (w *existsWatch) C() <-chan struct{} { return w.ch }

func (w *existsWatch) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	inner := w.inner
	w.inner = nil
	w.mu.Unlock()
	if inner != nil {
		return inner.Close()
	}
	return nil
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
			if !w.rearm("") {
				return
			}
			continue
		}
		w.signal()
		dir, _, err := resolveExistsWatch(w.target)
		if err != nil {
			return
		}
		if dir != w.currentDir() {
			if !w.rearm(dir) {
				return
			}
		}
		if exists, err := Exists(Spec{Raw: w.target}); err == nil && exists {
			w.signal()
		}
	}
}

func (w *existsWatch) rearm(dir string) bool {
	if dir == "" {
		var err error
		dir, _, err = resolveExistsWatch(w.target)
		if err != nil {
			return false
		}
	}
	next, err := openDirWatch(dir, "")
	if err != nil {
		return false
	}
	if w.isClosed() {
		_ = next.Close()
		return false
	}
	old := w.swapInner(next, dir)
	if old != nil {
		_ = old.Close()
	}
	return true
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
	return w.inner
}

func (w *existsWatch) currentDir() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.watchDir
}

func (w *existsWatch) swapInner(inner Watch, dir string) Watch {
	w.mu.Lock()
	defer w.mu.Unlock()
	old := w.inner
	w.inner = inner
	w.watchDir = dir
	return old
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
	p, err := windows.UTF16PtrFromString(raw)
	if err != nil {
		return 0, err
	}
	return windows.GetFileAttributes(p)
}
