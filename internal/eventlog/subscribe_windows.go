//go:build windows

package eventlog

import (
	"fmt"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	evtSubscribeToFutureEvents = 1
	errorEvtInvalidChannelPath = 15000
	errorEvtChannelNotFound    = 15007
)

var (
	modWevtapi       = windows.NewLazySystemDLL("wevtapi.dll")
	procEvtSubscribe = modWevtapi.NewProc("EvtSubscribe")
	procEvtNext      = modWevtapi.NewProc("EvtNext")
	procEvtClose     = modWevtapi.NewProc("EvtClose")

	modAdvapi32               = windows.NewLazySystemDLL("advapi32.dll")
	procRegisterEventSourceW  = modAdvapi32.NewProc("RegisterEventSourceW")
	procReportEventW          = modAdvapi32.NewProc("ReportEventW")
	procDeregisterEventSource = modAdvapi32.NewProc("DeregisterEventSource")
)

type winSub struct {
	sub   windows.Handle
	event windows.Handle
	ch    chan struct{}

	mu     sync.Mutex
	closed bool
}

// OpenSubscribe watches channel for EventID via EvtSubscribe (push).
// An unknown channel returns ErrUnknownChannel.
func OpenSubscribe(t Trigger) (Subscription, error) {
	if t.Channel == "" || t.EventID == 0 {
		parsed, err := ParseTrigger(t.Raw)
		if err != nil {
			return nil, err
		}
		t = parsed
	}
	if err := modWevtapi.Load(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSubscribeFailed, err)
	}
	ev, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSubscribeFailed, err)
	}
	channel, err := windows.UTF16PtrFromString(t.Channel)
	if err != nil {
		_ = windows.CloseHandle(ev)
		return nil, err
	}
	query, err := windows.UTF16PtrFromString(t.Query())
	if err != nil {
		_ = windows.CloseHandle(ev)
		return nil, err
	}
	r0, _, callErr := procEvtSubscribe.Call(
		0,
		uintptr(ev),
		uintptr(unsafe.Pointer(channel)),
		uintptr(unsafe.Pointer(query)),
		0,
		0,
		0,
		evtSubscribeToFutureEvents,
	)
	if r0 == 0 {
		_ = windows.CloseHandle(ev)
		return nil, mapSubscribeErr(callErr, t)
	}
	s := &winSub{
		sub:   windows.Handle(r0),
		event: ev,
		ch:    make(chan struct{}, 8),
	}
	go s.loop()
	return s, nil
}

func (s *winSub) C() <-chan struct{} { return s.ch }

func (s *winSub) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	_ = windows.SetEvent(s.event)
	evtClose(s.sub)
	_ = windows.CloseHandle(s.event)
	return nil
}

func (s *winSub) loop() {
	defer close(s.ch)
	for {
		if s.isClosed() {
			return
		}
		if _, waitErr := windows.WaitForSingleObject(s.event, windows.INFINITE); waitErr != nil || s.isClosed() {
			return
		}
		if !s.drain() {
			return
		}
	}
}

func (s *winSub) drain() bool {
	var handles [8]windows.Handle
	for {
		if s.isClosed() {
			return false
		}
		var returned uint32
		r, _, err := procEvtNext.Call(
			uintptr(s.sub),
			uintptr(len(handles)),
			uintptr(unsafe.Pointer(&handles[0])),
			0,
			0,
			uintptr(unsafe.Pointer(&returned)),
		)
		if r == 0 {
			if errno, ok := err.(syscall.Errno); ok && errno == windows.ERROR_NO_MORE_ITEMS {
				return true
			}
			return false
		}
		for i := uint32(0); i < returned; i++ {
			select {
			case s.ch <- struct{}{}:
			default:
			}
			evtClose(handles[i])
		}
	}
}

func (s *winSub) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func evtClose(h windows.Handle) {
	if h == 0 {
		return
	}
	_, _, _ = procEvtClose.Call(uintptr(h))
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

// SubscribeOK reports whether this process can EvtSubscribe Application
// and ReportEvent. Windows live tests skip when this fails.
func SubscribeOK() error {
	if err := modWevtapi.Load(); err != nil {
		return fmt.Errorf("cannot EvtSubscribe: %w", err)
	}
	t := Trigger{Channel: "Application", EventID: 65001, Raw: "Application:EventID=65001"}
	s, err := OpenSubscribe(t)
	if err != nil {
		return fmt.Errorf("cannot EvtSubscribe: %w", err)
	}
	_ = s.Close()
	if err := ReportApplicationEvent(65002, "winunitd t2 probe"); err != nil {
		return fmt.Errorf("cannot ReportEvent: %w", err)
	}
	return nil
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
	strings := [1]*uint16{msg}
	r, _, reportErr := procReportEventW.Call(
		h,
		eventlogInformationType,
		0,
		uintptr(eventID),
		0,
		1,
		0,
		uintptr(unsafe.Pointer(&strings[0])),
		0,
	)
	if r == 0 {
		return fmt.Errorf("ReportEvent: %v", reportErr)
	}
	return nil
}
