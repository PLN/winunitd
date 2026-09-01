package journal

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestJournalWriteAndRead(t *testing.T) {
	t.Parallel()
	s := testStore(t)

	stdout, stdoutW := io.Pipe()
	stderr, stderrW := io.Pipe()
	inv := NewInvocationID()
	s.Attach("foo.service", 4216, inv, stdout, stderr)

	if _, err := io.WriteString(stdoutW, "hello stdout\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(stderrW, "hello stderr\n"); err != nil {
		t.Fatal(err)
	}
	if err := stdoutW.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stderrW.Close(); err != nil {
		t.Fatal(err)
	}

	got := waitEntries(t, s, "foo.service", 2)
	msgs := map[string]Entry{}
	for _, e := range got {
		msgs[e.Message] = e
		if e.Unit != "foo.service" {
			t.Fatalf("unit = %q", e.Unit)
		}
		if e.PID != 4216 {
			t.Fatalf("pid = %d", e.PID)
		}
		if e.InvocationID != inv {
			t.Fatalf("invocation id = %q, want %q", e.InvocationID, inv)
		}
		if e.Timestamp.IsZero() {
			t.Fatal("missing timestamp")
		}
	}
	if msgs["hello stdout"].Stream != "stdout" {
		t.Fatalf("stdout entry = %+v", msgs["hello stdout"])
	}
	if msgs["hello stderr"].Stream != "stderr" {
		t.Fatalf("stderr entry = %+v", msgs["hello stderr"])
	}

	path := filepath.Join(s.Dir(), "foo.service.log")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("per-unit file: %v", err)
	}
}

func TestJournalSurvivesReopen(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	stdout, w := io.Pipe()
	s.Attach("bar.service", 7, NewInvocationID(), stdout, nil)
	if _, err := io.WriteString(w, "persisted line\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	s.Wait("bar.service")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	got, err := s2.Read("bar.service")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Message != "persisted line" {
		t.Fatalf("reopen = %+v", got)
	}
}

func TestJournalReadMissingUnit(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	got, err := s.Read("missing.service")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("missing = %+v", got)
	}
}

func TestJournalSkipsEmptyLines(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	r, w := io.Pipe()
	s.Attach("foo.service", 1, NewInvocationID(), r, nil)
	if _, err := io.WriteString(w, "\nkeep me\n\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got := waitEntries(t, s, "foo.service", 1)
	if got[0].Message != "keep me" {
		t.Fatalf("got = %+v", got)
	}
}

func TestStoreAttachNil(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	s.Attach("foo.service", 0, NewInvocationID(), nil, strings.NewReader(""))
	(*Store)(nil).Attach("foo.service", 0, "", nil, strings.NewReader("x"))
}

func TestAttachUsesCallerInvocationID(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	id1 := "11111111-1111-4111-8111-111111111111"
	id2 := "22222222-2222-4222-8222-222222222222"

	r1, w1 := io.Pipe()
	s.Attach("foo.service", 1, id1, r1, nil)
	if _, err := io.WriteString(w1, "first run\n"); err != nil {
		t.Fatal(err)
	}
	if err := w1.Close(); err != nil {
		t.Fatal(err)
	}
	_ = waitEntries(t, s, "foo.service", 1)

	r2, w2 := io.Pipe()
	s.Attach("foo.service", 2, id2, r2, nil)
	if _, err := io.WriteString(w2, "second run\n"); err != nil {
		t.Fatal(err)
	}
	if err := w2.Close(); err != nil {
		t.Fatal(err)
	}
	got := waitEntries(t, s, "foo.service", 2)
	if got[0].Message != "first run" || got[0].InvocationID != id1 {
		t.Fatalf("first = %+v", got[0])
	}
	if got[1].Message != "second run" || got[1].InvocationID != id2 {
		t.Fatalf("second = %+v", got[1])
	}
	if got[1].InvocationID == got[0].InvocationID {
		t.Fatal("second run must not share the first invocation id")
	}
}

func TestWaitAfterCloseFlushesCapture(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	inv := NewInvocationID()
	r, w := io.Pipe()
	s.Attach("foo.service", 1, inv, r, nil)
	if _, err := io.WriteString(w, "late line\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	s.Wait("foo.service")
	got, err := s.Read("foo.service")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Message != "late line" || got[0].InvocationID != inv {
		t.Fatalf("got = %+v", got)
	}
}

func TestJournalCaseVariantsShareOneFile(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	stdout, w := io.Pipe()
	s.Attach("FOO.SERVICE", 9, NewInvocationID(), stdout, nil)
	if _, err := io.WriteString(w, "from upper\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	s.Wait("foo.service")
	got := waitEntries(t, s, "Foo.service", 1)
	if got[0].Unit != "foo.service" || got[0].Message != "from upper" {
		t.Fatalf("got = %+v", got)
	}
	path := filepath.Join(s.Dir(), "foo.service.log")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("journal file: %v", err)
	}
	ents, err := os.ReadDir(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	logs := 0
	for _, e := range ents {
		if strings.HasSuffix(strings.ToLower(e.Name()), ".log") {
			logs++
		}
	}
	if logs != 1 {
		t.Fatalf("journal files = %d, want 1", logs)
	}
}

func TestOpenRequiresDir(t *testing.T) {
	t.Parallel()
	if _, err := Open(""); err == nil {
		t.Fatal("expected error")
	}
}

func TestUnitFileNameEncoding(t *testing.T) {
	t.Parallel()
	if unitFileName("foo.service") != "foo.service.log" {
		t.Fatalf("got %q", unitFileName("foo.service"))
	}
	if unitFileName("FOO.SERVICE") != "foo.service.log" {
		t.Fatalf("mixed case = %q", unitFileName("FOO.SERVICE"))
	}
	if unitFileName("foo:bar") == unitFileName("foo*bar") {
		t.Fatal("colon and star must not collide")
	}
	if unitFileName("foo:bar") != "foo%3Abar.log" {
		t.Fatalf("colon = %q", unitFileName("foo:bar"))
	}
	if unitFileName("foo*bar") != "foo%2Abar.log" {
		t.Fatalf("star = %q", unitFileName("foo*bar"))
	}
	if unitFileName(`foo/../bar:baz`) != "foo%2F..%2Fbar%3Abaz.log" {
		t.Fatalf("got %q", unitFileName(`foo/../bar:baz`))
	}
}

func TestNoCrossUnitBleed(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	colon := "foo:bar.service"
	star := "foo*bar.service"
	appendLine(t, s, colon, "from colon")
	appendLine(t, s, star, "from star")

	gotColon, err := s.Read(colon)
	if err != nil {
		t.Fatal(err)
	}
	gotStar, err := s.Read(star)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotColon) != 1 || gotColon[0].Message != "from colon" || gotColon[0].Unit != canonicalUnit(colon) {
		t.Fatalf("colon = %+v", gotColon)
	}
	if len(gotStar) != 1 || gotStar[0].Message != "from star" || gotStar[0].Unit != canonicalUnit(star) {
		t.Fatalf("star = %+v", gotStar)
	}

	// Defense in depth: mixed records in one file are filtered on read.
	path := s.path("mixed.service")
	recOther, _ := json.Marshal(record{V: 1, Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Unit: "other.service", Message: "bleed"})
	recMine, _ := json.Marshal(record{V: 1, Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Unit: "mixed.service", Message: "keep"})
	body := string(recOther) + "\n" + string(recMine) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := s.Read("mixed.service")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Message != "keep" {
		t.Fatalf("filter = %+v", got)
	}
}

func TestRotateKeepsGenerations(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	s.maxSize = 400
	s.keep = 3
	for i := 0; i < 40; i++ {
		s.append(Entry{
			Timestamp: time.Now().UTC(),
			Unit:      "rot.service",
			PID:       1,
			Stream:    "stdout",
			Message:   strings.Repeat("x", 80) + strconv.Itoa(i),
		})
	}
	s.syncUnit("rot.service")

	base := s.path("rot.service")
	if _, err := os.Stat(base); err != nil {
		t.Fatalf("current: %v", err)
	}
	if _, err := os.Stat(base + ".1"); err != nil {
		t.Fatalf("rotated .1: %v", err)
	}
	st, err := os.Stat(base)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() > s.maxSize {
		t.Fatalf("current size %d exceeds cap %d", st.Size(), s.maxSize)
	}

	got, err := s.Read("rot.service")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 {
		t.Fatalf("rotated read = %d lines", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].Message == got[i-1].Message && got[i].Timestamp.Before(got[i-1].Timestamp) {
			t.Fatalf("out of order: %+v then %+v", got[i-1], got[i])
		}
	}
}

func TestQuerySinceAndCursor(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	t3 := t2.Add(time.Hour)
	s.append(Entry{Timestamp: t1, Unit: "foo.service", Message: "old"})
	s.append(Entry{Timestamp: t2, Unit: "foo.service", Message: "mid"})
	s.append(Entry{Timestamp: t3, Unit: "foo.service", Message: "new"})

	got, _, err := s.Query("foo.service", t2, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Message != "mid" || got[1].Message != "new" {
		t.Fatalf("since = %+v", got)
	}

	all, cur, err := s.Query("foo.service", time.Time{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || cur == "" {
		t.Fatalf("all = %+v cursor %q", all, cur)
	}
	more, _, err := s.Query("foo.service", time.Time{}, cur)
	if err != nil {
		t.Fatal(err)
	}
	if len(more) != 0 {
		t.Fatalf("after cursor = %+v", more)
	}

	first, cur1, err := s.Query("foo.service", time.Time{}, "")
	if err != nil || len(first) < 1 {
		t.Fatalf("first = %+v %v", first, err)
	}
	rest, _, err := s.Query("foo.service", time.Time{}, formatCursor(entryID(first[0]), 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 2 || rest[0].Message != "mid" || rest[1].Message != "new" {
		t.Fatalf("rest = %+v (cur1=%s)", rest, cur1)
	}

	dupT := t3.Add(time.Hour)
	s.append(Entry{Timestamp: dupT, Unit: "foo.service", Message: "same"})
	s.append(Entry{Timestamp: dupT, Unit: "foo.service", Message: "same"})
	allDup, dupCur, err := s.Query("foo.service", dupT, "")
	if err != nil || len(allDup) != 2 {
		t.Fatalf("dups = %+v %v", allDup, err)
	}
	afterDup, _, err := s.Query("foo.service", dupT, dupCur)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterDup) != 0 {
		t.Fatalf("duplicate cursor replay = %+v", afterDup)
	}
}

func appendLine(t *testing.T, s *Store, unit, msg string) {
	t.Helper()
	s.append(Entry{
		Timestamp: time.Now().UTC(),
		Unit:      unit,
		Message:   msg,
	})
}

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func waitEntries(t *testing.T, s *Store, unit string, n int) []Entry {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var got []Entry
	var err error
	for time.Now().Before(deadline) {
		got, err = s.Read(unit)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) >= n {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("entries = %+v, want >= %d", got, n)
	return nil
}
