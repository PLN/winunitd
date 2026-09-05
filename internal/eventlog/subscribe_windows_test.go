//go:build windows

package eventlog

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsOpenSubscribeFiresOnReport(t *testing.T) {
	if err := SubscribeOK(); err != nil {
		t.Skip(err.Error())
	}
	id := testEventID(t)
	tr, err := ParseTrigger(applicationTrigger(id))
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenSubscribe(tr)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := ReportApplicationEvent(id, "winunitd t2 subscribe test"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.C():
	case <-time.After(8 * time.Second):
		t.Fatal("Application subscribe did not fire after ReportEvent")
	}
}

func TestWindowsOpenSubscribeQueryIsEventIDXPath(t *testing.T) {
	if err := WevtapiOK(); err != nil {
		t.Skip(err.Error())
	}
	tr, err := ParseTrigger("Application:EventID=4242")
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenSubscribe(tr)
	if err != nil {
		t.Skip(err.Error())
	}
	defer s.Close()
	ws, ok := s.(*winSub)
	if !ok {
		t.Fatalf("OpenSubscribe type %T", s)
	}
	got := windows.UTF16ToString(ws.queryUTF16)
	if got != "*[System[(EventID=4242)]]" {
		t.Fatalf("EvtSubscribe query = %q (must not be NULL)", got)
	}
}

func TestWindowsCallbackDeliverSignalsWithoutXML(t *testing.T) {
	s := &winSub{ch: make(chan struct{}, 1)}
	id := registerSub(s)
	t.Cleanup(func() {
		unregisterSub(id)
	})
	if evtSubscribeCallback(evtSubscribeActionDeliver, id, 0) != 0 {
		t.Fatal("callback")
	}
	select {
	case <-s.ch:
	default:
		t.Fatal("deliver must signal without EvtRender of the event handle")
	}
}

func TestWindowsOpenSubscribeUnknownChannel(t *testing.T) {
	if err := WevtapiOK(); err != nil {
		t.Skip(err.Error())
	}
	tr, err := ParseTrigger("winunitd-no-such-channel:EventID=1")
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenSubscribe(tr)
	if err == nil || s != nil {
		if s != nil {
			_ = s.Close()
		}
		t.Fatal("unknown channel must fail subscribe")
	}
	if !strings.Contains(err.Error(), "unknown") && !strings.Contains(err.Error(), "subscribe") {
		t.Fatalf("err = %v", err)
	}
}

func testEventID(t *testing.T) uint16 {
	t.Helper()
	id := uint16(40000 + time.Now().UnixNano()%20000)
	if id == 0 {
		id = 40001
	}
	return id
}

func applicationTrigger(id uint16) string {
	return fmt.Sprintf("Application:EventID=%d", id)
}

func TestSubscriptionCloseRetainsFailureAndSerializesRetry(t *testing.T) {
	failure := errors.New("injected EvtClose failure")
	var calls atomic.Int32
	s := &winSub{sub: 42, ch: make(chan struct{}, 1)}
	s.closeNative = func(windows.Handle) error {
		if calls.Add(1) == 1 {
			return failure
		}
		return nil
	}
	id := registerSub(s)
	if err := s.Close(); !errors.Is(err, failure) {
		t.Fatalf("close result: %v", err)
	}
	if s.sub != 42 {
		t.Fatal("failed EvtClose discarded handle")
	}
	if _, ok := <-s.C(); ok {
		t.Fatal("failed close kept event admission open")
	}
	evtSubscribeCallback(evtSubscribeActionDeliver, id, 0)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- s.Close() }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if s.sub != 0 || calls.Load() != 2 {
		t.Fatal("concurrent retries duplicated native close or lost handle")
	}
}

func TestSubscriptionCallbackFailureAndCloseShareChannelClosure(t *testing.T) {
	for i := 0; i < 100; i++ {
		s := &winSub{ch: make(chan struct{}, 1)}
		id := registerSub(s)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); evtSubscribeCallback(evtSubscribeActionError, id, 0) }()
		go func() { defer wg.Done(); _ = s.Close() }()
		wg.Wait()
		if _, ok := <-s.C(); ok {
			t.Fatal("channel still open")
		}
	}
}

func TestSubscriptionNativeCloseIsIdempotent(t *testing.T) {
	tr, err := ParseTrigger("Application:EventID=4242")
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenSubscribe(tr)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal("repeated native close", err)
	}
	if s.(*winSub).sub != 0 {
		t.Fatal("successful native close retained handle")
	}
}
