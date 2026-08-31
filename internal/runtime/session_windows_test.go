//go:build windows

package runtime

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

func TestParseSessionChangeLogonLogoff(t *testing.T) {
	n := windows.WTSSESSION_NOTIFICATION{Size: 8, SessionID: 3}
	ptr := uintptr(unsafe.Pointer(&n))

	sc, ok := ParseSessionChange(svc.SessionChange, windows.WTS_SESSION_LOGON, ptr)
	if !ok || !sc.Logon || sc.SessionID != 3 {
		t.Fatalf("logon = %+v ok=%v", sc, ok)
	}
	sc, ok = ParseSessionChange(svc.SessionChange, windows.WTS_SESSION_LOGOFF, ptr)
	if !ok || sc.Logon || sc.SessionID != 3 {
		t.Fatalf("logoff = %+v ok=%v", sc, ok)
	}
	_, ok = ParseSessionChange(svc.SessionChange, windows.WTS_CONSOLE_DISCONNECT, ptr)
	if ok {
		t.Fatal("disconnect is not logoff")
	}
	_, ok = ParseSessionChange(svc.SessionChange, windows.WTS_SESSION_LOCK, ptr)
	if ok {
		t.Fatal("lock is not logoff")
	}
	_, ok = ParseSessionChange(svc.Stop, windows.WTS_SESSION_LOGON, ptr)
	if ok {
		t.Fatal("stop is not a session change")
	}
	sc, ok = ParseSessionChange(svc.SessionChange, windows.WTS_CONSOLE_CONNECT, ptr)
	if !ok || !sc.Logon {
		t.Fatalf("console connect should start a manager if needed: %+v", sc)
	}
}

func TestInteractiveSessionsSkipsSessionZeroShape(t *testing.T) {
	ids, err := InteractiveSessions()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if id == 0 {
			t.Fatal("session 0 must not be treated as interactive")
		}
	}
}

func TestSIDHasInteractiveSessionRejectsNonInteractiveSID(t *testing.T) {
	if SIDHasInteractiveSession("S-1-5-18") {
		t.Fatal("Local System must not report an interactive session")
	}
	if SIDHasInteractiveSession("") {
		t.Fatal("empty SID")
	}
}
