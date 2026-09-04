package journal

import (
	"encoding/json"
	"testing"
	"time"
)

func TestQueryPagePreservesDuplicateCursor(t *testing.T) {
	s := testStore(t)
	e := Entry{Unit: "foo.service", Timestamp: time.Now().UTC(), Message: "same"}
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		s.append(e)
	}
	cursor := ""
	for i := 0; i < 5; i++ {
		entries, next, more, err := s.QueryPage(e.Unit, time.Time{}, cursor, len(raw)+1)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || next == cursor || more != (i < 4) {
			t.Fatalf("page %d: entries=%d cursor=%q more=%v", i, len(entries), next, more)
		}
		cursor = next
	}
}

func TestQueryPageRejectsOversizedEntry(t *testing.T) {
	s := testStore(t)
	s.append(Entry{Unit: "foo.service", Message: "oversized"})
	_, _, _, err := s.QueryPage("foo.service", time.Time{}, "", 1)
	if err == nil {
		t.Fatal("oversized entry must return an error")
	}
}
