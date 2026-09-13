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
	closeMu sync.Mutex
	done    chan struct{}
	key     registry.Key
	event   windows.Handle
	ch      chan struct{}

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
	// READ includes KEY_NOTIFY. NOTIFY alone misses value changes on some
	// hives (notably HKCU) until the first RegNotifyChangeKeyValue is armed.
	k, err := registry.OpenKey(root, key.Path, registry.READ)
	if err != nil {
		if isMissingKey(err) {
			return nil, fmt.Errorf("%w: %s", ErrMissingKey, key.Raw)
		}
		return nil, err
	}
	ev, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		return failedOpenWatch(&winWatch{key: k, ch: make(chan struct{}), done: make(chan struct{})}, err)
	}
	w := &winWatch{
		key:   k,
		event: ev,
		ch:    make(chan struct{}, 1),
		done:  make(chan struct{}),
	}
	if err := w.arm(); err != nil {
		return failedOpenWatch(w, err)
	}
	go w.loop()
	return w, nil
}

// No loop has started. Return unfinished handles with the open error so the
// caller can retain ownership and retry cleanup.
func failedOpenWatch(w *winWatch, openErr error) (Watch, error) {
	close(w.done)
	close(w.ch)
	if err := w.Close(); err != nil {
		return w, errors.Join(openErr, err)
	}
	return nil, openErr
}

func (w *winWatch) C() <-chan struct{} { return w.ch }

func (w *winWatch) Close() error {
	w.closeMu.Lock()
	defer w.closeMu.Unlock()
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	if w.event != 0 {
		if err := windows.SetEvent(w.event); err != nil {
			return fmt.Errorf("wake registry watch: %w", err)
		}
	}
	// The waiter and rearm operation must leave before their handles close.
	// Closing a handle while WaitForSingleObject is pending is undefined.
	<-w.done
	if w.key != 0 {
		if err := w.key.Close(); err != nil {
			return fmt.Errorf("close registry key: %w", err)
		}
		w.key = 0
	}
	if w.event != 0 {
		if err := windows.CloseHandle(w.event); err != nil {
			return fmt.Errorf("close registry event: %w", err)
		}
		w.event = 0
	}
	return nil
}

func (w *winWatch) arm() error {
	return windows.RegNotifyChangeKeyValue(
		windows.Handle(w.key),
		true,
		notifyFilter,
		w.event,
		true,
	)
}

func (w *winWatch) loop() {
	defer close(w.done)
	defer close(w.ch)
	for {
		if w.isClosed() {
			return
		}
		if _, waitErr := windows.WaitForSingleObject(w.event, windows.INFINITE); waitErr != nil || w.isClosed() {
			return
		}
		select {
		case w.ch <- struct{}{}:
		default:
		}
		if err := w.arm(); err != nil || w.isClosed() {
			return
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

// UserHiveWatchOK checks a real user token and its loaded profile hive.
// Headless S4U users can run in session 0; the session number is not identity.
func UserHiveWatchOK() error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("cannot open HKCU: %w", err)
	}
	sid := user.User.Sid
	if sid.IsWellKnown(windows.WinLocalSystemSid) || sid.IsWellKnown(windows.WinLocalServiceSid) || sid.IsWellKnown(windows.WinNetworkServiceSid) {
		return fmt.Errorf("cannot qualify HKCU with a built-in service identity")
	}
	// Require the user's actual loaded hive rather than accepting a fallback hive.
	loaded, err := registry.OpenKey(registry.USERS, sid.String(), registry.READ)
	if err != nil {
		return fmt.Errorf("user profile hive is not loaded: %w", err)
	}
	if err := loaded.Close(); err != nil {
		return err
	}
	current, err := registry.OpenKey(registry.CURRENT_USER, "", registry.NOTIFY)
	if err != nil {
		return fmt.Errorf("cannot open HKCU: %w", err)
	}
	return current.Close()
}
