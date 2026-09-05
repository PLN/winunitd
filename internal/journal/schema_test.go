package journal

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestEncodeDecodeCurrentVersion(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	ts := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	s.append(Entry{
		Timestamp:    ts,
		Unit:         "foo.service",
		PID:          4216,
		Stream:       "stdout",
		Message:      "hello",
		InvocationID: "11111111-1111-4111-8111-111111111111",
		Severity:     SeverityInfo,
		Session:      "3",
		UserSID:      "S-1-5-21-1-2-3-1001",
	})
	s.syncUnit("foo.service")

	raw, err := os.ReadFile(s.path("foo.service"))
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(raw))
	var rec record
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("decode wire: %v\n%s", err, line)
	}
	if rec.V != FormatVersion {
		t.Fatalf("v = %d, want %d", rec.V, FormatVersion)
	}
	if rec.Severity != SeverityInfo || rec.Session != "3" || rec.UserSID != "S-1-5-21-1-2-3-1001" {
		t.Fatalf("v=2 fields = %+v", rec)
	}
	if rec.Timestamp == "" || rec.Unit != "foo.service" || rec.PID != 4216 || rec.Stream != "stdout" || rec.Message != "hello" || rec.InvocationID == "" {
		t.Fatalf("v=1 fields lost: %+v", rec)
	}

	s.append(Entry{
		Timestamp: ts.Add(time.Second),
		Unit:      "foo.service",
		Stream:    "stderr",
		Message:   "warn",
	})
	s.syncUnit("foo.service")
	raw, err = os.ReadFile(s.path("foo.service"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %d", len(lines))
	}
	var rec2 map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &rec2); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"severity", "session", "userSid"} {
		if _, ok := rec2[k]; !ok {
			t.Fatalf("v=2 line missing %s: %s", k, lines[1])
		}
	}
	if rec2["v"] != float64(FormatVersion) {
		t.Fatalf("v = %v", rec2["v"])
	}
	if rec2["severity"] != SeverityErr {
		t.Fatalf("stderr severity = %v", rec2["severity"])
	}
	if rec2["session"] != "" || rec2["userSid"] != "" {
		t.Fatalf("unknown origin must be empty: %s", lines[1])
	}

	got, err := s.Read("foo.service")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("read = %+v", got)
	}
	if got[0].Severity != SeverityInfo || got[0].Session != "3" || got[0].UserSID != "S-1-5-21-1-2-3-1001" {
		t.Fatalf("round-trip first = %+v", got[0])
	}
	if got[1].Severity != SeverityErr || got[1].Session != "" || got[1].UserSID != "" {
		t.Fatalf("round-trip second = %+v", got[1])
	}
}

func TestMixedV1V2Read(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	ts1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ts2 := ts1.Add(time.Hour)
	v1, err := json.Marshal(map[string]any{
		"v":            1,
		"timestamp":    ts1.Format(time.RFC3339Nano),
		"unit":         "foo.service",
		"pid":          7,
		"stream":       "stdout",
		"message":      "old line",
		"invocationId": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	})
	if err != nil {
		t.Fatal(err)
	}
	v2, err := json.Marshal(record{
		V:            2,
		Timestamp:    ts2.Format(time.RFC3339Nano),
		Unit:         "foo.service",
		PID:          8,
		Stream:       "stderr",
		Message:      "new line",
		InvocationID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		Severity:     SeverityErr,
		Session:      "4",
		UserSID:      "S-1-5-21-1-2-3-1002",
	})
	if err != nil {
		t.Fatal(err)
	}
	body := string(v1) + "\n" + string(v2) + "\n"
	if err := os.WriteFile(s.path("foo.service"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := s.Read("foo.service")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("mixed = %+v", got)
	}
	old := got[0]
	if old.Message != "old line" || old.PID != 7 || old.Stream != "stdout" || old.InvocationID == "" {
		t.Fatalf("v=1 identity = %+v", old)
	}
	if old.Severity != "" || old.Session != "" || old.UserSID != "" {
		t.Fatalf("v=1 new fields must be empty: %+v", old)
	}
	newE := got[1]
	if newE.Message != "new line" || newE.Severity != SeverityErr || newE.Session != "4" || newE.UserSID != "S-1-5-21-1-2-3-1002" {
		t.Fatalf("v=2 = %+v", newE)
	}

	since, cur, err := s.Query("foo.service", ts2, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(since) != 1 || since[0].Message != "new line" || cur == "" {
		t.Fatalf("since v=2 = %+v %q", since, cur)
	}
}

func TestSeverityFromStream(t *testing.T) {
	t.Parallel()
	cases := []struct {
		stream, want string
	}{
		{"stdout", SeverityInfo},
		{"stderr", SeverityErr},
		{"", ""},
		{"other", ""},
	}
	for _, tc := range cases {
		if got := SeverityFromStream(tc.stream); got != tc.want {
			t.Errorf("SeverityFromStream(%q) = %q, want %q", tc.stream, got, tc.want)
		}
	}

	s := testStore(t)
	s.append(Entry{Timestamp: time.Now().UTC(), Unit: "foo.service", Stream: "stdout", Message: "out"})
	s.append(Entry{Timestamp: time.Now().UTC(), Unit: "foo.service", Stream: "stderr", Message: "err"})
	s.append(Entry{Timestamp: time.Now().UTC(), Unit: "foo.service", Stream: "stdout", Message: "override", Severity: "debug"})
	got := waitEntries(t, s, "foo.service", 3)
	if got[0].Severity != SeverityInfo || got[1].Severity != SeverityErr || got[2].Severity != "debug" {
		t.Fatalf("stored severity = %+v", got)
	}
}

func TestOriginSIDAndSession(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	s.SetOrigin(Origin{Session: "3", UserSID: "S-1-5-21-1-2-3-1001"})
	stdout, w := io.Pipe()
	s.Attach("foo.service", 9, NewInvocationID(), stdout, nil)
	if _, err := io.WriteString(w, "from user\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got := waitEntries(t, s, "foo.service", 1)
	if got[0].Session != "3" || got[0].UserSID != "S-1-5-21-1-2-3-1001" {
		t.Fatalf("origin = %+v", got[0])
	}
	if got[0].Severity != SeverityInfo {
		t.Fatalf("stdout severity = %q", got[0].Severity)
	}

	s2 := testStore(t)
	cases := []Origin{
		{},
		{Session: "7"},
		{UserSID: "S-1-5-21-9"},
		{Session: "2", UserSID: "S-1-5-21-1-2-3-1003"},
	}
	for _, o := range cases {
		s2.append(Entry{
			Timestamp: time.Now().UTC(),
			Unit:      "bar.service",
			Stream:    "stderr",
			Message:   "line",
			Session:   o.Session,
			UserSID:   o.UserSID,
		})
	}
	got, err := s2.Read("bar.service")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(cases) {
		t.Fatalf("got %d", len(got))
	}
	for i, o := range cases {
		if got[i].Session != o.Session || got[i].UserSID != o.UserSID {
			t.Fatalf("case %d = %+v, want session %q sid %q", i, got[i], o.Session, o.UserSID)
		}
		if got[i].Severity != SeverityErr {
			t.Fatalf("case %d severity = %q", i, got[i].Severity)
		}
	}
}
