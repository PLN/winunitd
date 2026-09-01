//go:build windows

package registry

import (
	"errors"
	"fmt"
	"sync"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const notifyFilter = windows.REG_NOTIFY_CHANGE_NAME |
	windows.REG_NOTIFY_CHANGE_ATTRIBUTES |
	windows.REG_NOTIFY_CHANGE_LAST_SET |
	windows.REG_NOTIFY_CHANGE_SECURITY

type winWatch struct {
	key   registry.Key
	event windows.Handle
	ch    chan struct{}

	mu     sync.Mutex
	closed bool
}

// OpenWatch opens key with KEY_NOTIFY and watches the key plus its subtree
// via RegNotifyChangeKeyValue. A missing key returns ErrMissingKey.
func OpenWatch(key Key) (Watch, error) {
	root, err := hiveRoot(key.Hive)
	if err != nil {
		return nil, err
	}
	k, err := registry.OpenKey(root, key.Path, registry.NOTIFY)
	if err != nil {
		if isMissingKey(err) {
			return nil, fmt.Errorf("%w: %s", ErrMissingKey, key.Raw)
		}
		return nil, err
	}
	ev, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		_ = k.Close()
		return nil, err
	}
	w := &winWatch{
		key:   k,
		event: ev,
		ch:    make(chan struct{}, 1),
	}
	go w.loop()
	return w, nil
}

func (w *winWatch) C() <-chan struct{} { return w.ch }

func (w *winWatch) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	w.mu.Unlock()
	_ = windows.SetEvent(w.event)
	_ = w.key.Close()
	_ = windows.CloseHandle(w.event)
	return nil
}

func (w *winWatch) loop() {
	defer close(w.ch)
	for {
		if w.isClosed() {
			return
		}
		err := windows.RegNotifyChangeKeyValue(
			windows.Handle(w.key),
			true,
			notifyFilter,
			w.event,
			true,
		)
		if err != nil || w.isClosed() {
			return
		}
		if _, waitErr := windows.WaitForSingleObject(w.event, windows.INFINITE); waitErr != nil || w.isClosed() {
			return
		}
		select {
		case w.ch <- struct{}{}:
		default:
		}
	}
}

func (w *winWatch) isClosed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}

func hiveRoot(h Hive) (registry.Key, error) {
	switch h {
	case HKLM:
		return registry.LOCAL_MACHINE, nil
	case HKCU:
		return registry.CURRENT_USER, nil
	default:
		return 0, fmt.Errorf("unsupported hive")
	}
}

func isMissingKey(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, windows.ERROR_FILE_NOT_FOUND) ||
		errors.Is(err, windows.ERROR_PATH_NOT_FOUND)
}
