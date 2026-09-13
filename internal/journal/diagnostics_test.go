package journal

import (
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func TestDiagnosticsBoundStorageStallAndPreserveIdentity(t *testing.T) {
	s := testStore(t)
	blocked, release := make(chan struct{}), make(chan struct{})
	var entered, released sync.Once
	unblock := func() { released.Do(func() { close(release) }) }
	defer unblock()
	s.onOpen = func() { entered.Do(func() { close(blocked); <-release }) }
	s.SetOrigin(Origin{Session: "1", UserSID: "S-1-5-21-1-2-3-1001"})
	s.RecordDiagnostic("work.service", "inv-1", "first rejection")
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("diagnostic did not reach blocked storage")
	}
	produced := make(chan struct{})
	go func() {
		s.SetOrigin(Origin{Session: "2", UserSID: "S-1-5-21-1-2-3-1002"})
		message := strings.Repeat("x", maxDiagnosticMessage-1) + "界"
		for i := 0; i < invocationQueueBytes/maxDiagnosticMessage+4; i++ {
			s.RecordDiagnostic("work.service", "inv-2", message)
		}
		close(produced)
	}()
	select {
	case <-produced:
	case <-time.After(time.Second):
		t.Fatal("diagnostic admission or origin publication waited for storage")
	}
	if s.diagnostics.bytes.Load() > invocationQueueBytes || s.diagnostics.records.Load() > invocationQueueRecords {
		t.Fatal("daemon diagnostics escaped capture admission bounds")
	}
	if stats := s.CaptureStats("work.service"); stats.DroppedRecords == 0 || stats.DroppedBytes == 0 {
		t.Fatal("diagnostic overload was not reported")
	}
	unblock()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := s.Read("work.service")
	if err != nil || len(entries) < 2 {
		t.Fatalf("persisted diagnostics: count=%d error=%v", len(entries), err)
	}
	for i, e := range entries {
		if e.Stream != "daemon" || e.Severity != SeverityErr || len(e.Message) > maxDiagnosticMessage || !utf8.ValidString(e.Message) {
			t.Fatal("diagnostic format or message bound changed")
		}
		if i == 0 {
			if e.InvocationID != "inv-1" || e.Session != "1" || e.UserSID != "S-1-5-21-1-2-3-1001" {
				t.Fatal("later origin changed an accepted diagnostic")
			}
		} else if e.InvocationID != "inv-2" || e.Session != "2" || e.UserSID != "S-1-5-21-1-2-3-1002" || !e.Partial {
			t.Fatal("new diagnostic did not capture its identity and truncation")
		}
	}
}
