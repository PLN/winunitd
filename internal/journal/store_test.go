package journal

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestJournalWriteAndRead(t *testing.T) {
	t.Parallel()
	s := testStore(t)

	stdout, stdoutW := io.Pipe()
	stderr, stderrW := io.Pipe()
	s.Attach("foo.service", 4216, stdout, stderr)

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
		if e.InvocationID == "" {
			t.Fatal("missing invocation id")
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
	s.Attach("bar.service", 7, stdout, nil)
	if _, err := io.WriteString(w, "persisted line\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	_ = waitEntries(t, s, "bar.service", 1)

	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
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
	s.Attach("foo.service", 1, r, nil)
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
	s.Attach("foo.service", 0, nil, strings.NewReader(""))
	(*Store)(nil).Attach("foo.service", 0, nil, strings.NewReader("x"))
}

func TestOpenRequiresDir(t *testing.T) {
	t.Parallel()
	if _, err := Open(""); err == nil {
		t.Fatal("expected error")
	}
}

func TestUnitFileNameSanitizes(t *testing.T) {
	t.Parallel()
	if unitFileName("foo.service") != "foo.service.log" {
		t.Fatalf("got %q", unitFileName("foo.service"))
	}
	if unitFileName(`foo/../bar:baz`) != "foo_.._bar_baz.log" {
		t.Fatalf("got %q", unitFileName(`foo/../bar:baz`))
	}
}

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
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
