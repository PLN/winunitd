//go:build windows

package eventlog

import (
	"fmt"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	evtSubscribeToFutureEvents = 1
	evtSubscribeActionError    = 0
	evtSubscribeActionDeliver  = 1
	errorEvtInvalidChannelPath = 15000
	errorEvtChannelNotFound    = 15007
)

var (
	modWevtapi       = windows.NewLazySystemDLL("wevtapi.dll")
	procEvtSubscribe = modWevtapi.NewProc("EvtSubscribe")
	procEvtClose     = modWevtapi.NewProc("EvtClose")

	modAdvapi32               = windows.NewLazySystemDLL("advapi32.dll")
	procRegisterEventSourceW  = modAdvapi32.NewProc("RegisterEventSourceW")
	procReportEventW          = modAdvapi32.NewProc("ReportEventW")
	procDeregisterEventSource = modAdvapi32.NewProc("DeregisterEventSource")

	// subscribeCallback is kept alive for EvtSubscribe (push).
	subscribeCallback = windows.NewCallback(evtSubscribeCallback)

	subMu   sync.Mutex
	subByID         = map[uintptr]*winSub{}
	subNext uintptr = 1
)

type winSub struct {
	closeMu     sync.Mutex
	channelOnce sync.Once
	closeNative func(windows.Handle) error // nil uses EvtClose; injectable failure tests
	id          uintptr
	sub         windows.Handle
	ch          chan struct{}
	queryUTF16  []uint16

	mu     sync.Mutex
	closed bool
}

// OpenSubscribe watches channel for EventID via EvtSubscribe (push callback).
// An unknown channel returns ErrUnknownChannel. A null or empty query
// returns ErrEmptyQuery (or a parse error); it does not match-all.
func OpenSubscribe(t Trigger) (Subscription, error) {
	resolved, query, err := subscribeQuery(t)
	if err != nil {
		return nil, err
	}
	t = resolved
	if err := modWevtapi.Load(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSubscribeFailed, err)
	}
	channel, err := windows.UTF16PtrFromString(t.Channel)
	if err != nil {
		return nil, err
	}
	queryUTF16, err := windows.UTF16FromString(query)
	if err != nil {
		return nil, err
	}

	s := &winSub{
		ch:         make(chan struct{}, 8),
		queryUTF16: queryUTF16,
	}
	id := registerSub(s)

	// EvtSubscribe applies *[System[(EventID=N)]]; the callback only
	// signals matches. Do not Query=NULL + EvtRender every event.
	r0, _, callErr := procEvtSubscribe.Call(
		0,
		0,
		uintptr(unsafe.Pointer(channel)),
		uintptr(unsafe.Pointer(&s.queryUTF16[0])),
		0,
		id,
		subscribeCallback,
		evtSubscribeToFutureEvents,
	)
	if r0 == 0 {
		unregisterSub(id)
		return nil, mapSubscribeErr(callErr, t)
	}
	s.sub = windows.Handle(r0)
	return s, nil
}

func (s *winSub) C() <-chan struct{} { return s.ch }

func (s *winSub) Close() error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	unregisterSub(s.id)
	s.channelOnce.Do(func() { close(s.ch) })
	if s.sub == 0 {
		return nil
	}
	// EvtClose waits for callbacks; holding s.mu here would deadlock them.
	closeNative := s.closeNative
	if closeNative == nil {
		closeNative = evtClose
	}
	if err := closeNative(s.sub); err != nil {
		return err
	}
	s.sub = 0
	return nil
}

func (s *winSub) signal() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.ch <- struct{}{}:
	default:
	}
}

func (s *winSub) fail() {
	// Called from the EvtSubscribe callback: do not EvtClose here.
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	unregisterSub(s.id)
	s.channelOnce.Do(func() { close(s.ch) })
}

func (s *winSub) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func evtSubscribeCallback(action, ctx, handle uintptr) uintptr {
	_ = handle // EventID filter is EvtSubscribe Query; do not EvtRender.
	subMu.Lock()
	s := subByID[ctx]
	subMu.Unlock()
	if s == nil || s.isClosed() {
		return 0
	}
	if action == evtSubscribeActionError {
		s.fail()
		return 0
	}
	if action != evtSubscribeActionDeliver {
		return 0
	}
	s.signal()
	return 0
}

func registerSub(s *winSub) uintptr {
	subMu.Lock()
	defer subMu.Unlock()
	id := subNext
	subNext++
	s.id = id
	subByID[id] = s
	return id
}

func unregisterSub(id uintptr) {
	subMu.Lock()
	delete(subByID, id)
	subMu.Unlock()
}

func evtClose(h windows.Handle) error {
	if h == 0 {
		return nil
	}
	r0, _, err := procEvtClose.Call(uintptr(h))
	if r0 == 0 {
		if err == nil || err == windows.ERROR_SUCCESS {
			return fmt.Errorf("EvtClose failed")
		}
		return fmt.Errorf("EvtClose: %w", err)
	}
	return nil
}

func mapSubscribeErr(err error, t Trigger) error {
	if errno, ok := err.(syscall.Errno); ok {
		switch errno {
		case errorEvtInvalidChannelPath, errorEvtChannelNotFound:
			return fmt.Errorf("%w: %s", ErrUnknownChannel, t.Channel)
		}
	}
	if err == nil || err == windows.ERROR_SUCCESS {
		return fmt.Errorf("%w: %s", ErrUnknownChannel, t.Channel)
	}
	return fmt.Errorf("%w: %v", ErrSubscribeFailed, err)
}

// SubscribeOK reports whether this process can EvtSubscribe Application,
// ReportEvent, and observe the matching event. Windows live tests skip
// when this fails.
func SubscribeOK() error {
	if err := modWevtapi.Load(); err != nil {
		return fmt.Errorf("cannot EvtSubscribe: %w", err)
	}
	id := uint16(65001)
	t := Trigger{Channel: "Application", EventID: id, Raw: "Application:EventID=65001"}
	s, err := OpenSubscribe(t)
	if err != nil {
		return fmt.Errorf("cannot EvtSubscribe: %w", err)
	}
	defer s.Close()
	if err := ReportApplicationEvent(id, "winunitd t2 probe"); err != nil {
		return fmt.Errorf("cannot ReportEvent: %w", err)
	}
	select {
	case <-s.C():
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("cannot EvtSubscribe: no event delivered after ReportEvent")
	}
}

// WevtapiOK reports whether wevtapi.dll loaded. Unknown-channel activate
// tests can still run when subscribe/report of Application is denied.
func WevtapiOK() error {
	if err := modWevtapi.Load(); err != nil {
		return fmt.Errorf("cannot load wevtapi: %w", err)
	}
	return nil
}

const eventlogInformationType = 4

// ReportApplicationEvent writes one event to the Application log.
func ReportApplicationEvent(eventID uint16, message string) error {
	if err := modAdvapi32.Load(); err != nil {
		return err
	}
	src, err := windows.UTF16PtrFromString("winunitd-t2")
	if err != nil {
		return err
	}
	h, _, callErr := procRegisterEventSourceW.Call(0, uintptr(unsafe.Pointer(src)))
	if h == 0 {
		return fmt.Errorf("RegisterEventSource: %v", callErr)
	}
	defer procDeregisterEventSource.Call(h)

	if message == "" {
		message = "winunitd eventlog trigger"
	}
	msg, err := windows.UTF16PtrFromString(message)
	if err != nil {
		return err
	}
	inserts := [1]*uint16{msg}
	r, _, reportErr := procReportEventW.Call(
		h,
		eventlogInformationType,
		0,
		uintptr(eventID),
		0,
		1,
		0,
		uintptr(unsafe.Pointer(&inserts[0])),
		0,
	)
	if r == 0 {
		return fmt.Errorf("ReportEvent: %v", reportErr)
	}
	return nil
}
