//go:build windows

package eventlog

import (
	"fmt"
	"strings"
	"testing"
	"time"
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
