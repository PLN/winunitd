package journal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	qmark := "foo?bar.service"
	pipe := "foo|bar.service"
	appendLine(t, s, colon, "from colon")
	appendLine(t, s, star, "from star")
	appendLine(t, s, qmark, "from qmark")
	appendLine(t, s, pipe, "from pipe")

	names := map[string]string{
		colon: "from colon",
		star:  "from star",
		qmark: "from qmark",
		pipe:  "from pipe",
	}
	files := map[string]bool{}
	for unit, msg := range names {
		got, err := s.Read(unit)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Message != msg || got[0].Unit != canonicalUnit(unit) {
			t.Fatalf("%s = %+v", unit, got)
		}
		fn := unitFileName(unit)
		if files[fn] {
			t.Fatalf("filename collision %q", fn)
		}
		files[fn] = true
		if _, err := os.Stat(filepath.Join(s.Dir(), fn)); err != nil {
			t.Fatalf("%s file: %v", fn, err)
		}
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
	for _, suf := range []string{".1", ".2", ".3"} {
		st, err := os.Stat(base + suf)
		if err != nil {
			t.Fatalf("rotated %s: %v", suf, err)
		}
		if st.Size() > s.maxSize {
			t.Fatalf("%s size %d exceeds cap %d", suf, st.Size(), s.maxSize)
		}
	}
	if _, err := os.Stat(base + ".4"); !os.IsNotExist(err) {
		t.Fatalf("keep 3 must drop .4: %v", err)
	}

	got, err := s.Read("rot.service")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 {
		t.Fatalf("rotated read = %d lines", len(got))
	}
	if len(got) >= 40 {
		t.Fatalf("rotation did not drop old lines: %d", len(got))
	}
	last := got[len(got)-1].Message
	if !strings.HasSuffix(last, "39") {
		t.Fatalf("newest line lost: %q", last)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Timestamp.Before(got[i-1].Timestamp) {
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

func TestQueryFlushesThenUnlocksBeforeScan(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	s.flushEvery = time.Hour
	s.append(Entry{Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Unit: "foo.service", Message: "buffered"})

	started := make(chan struct{})
	release := make(chan struct{})
	var lockHeld atomic.Bool
	var once sync.Once
	s.onScan = func() {
		once.Do(func() {
			if f := s.fileExisting("foo.service"); f != nil {
				if !f.mu.TryLock() {
					lockHeld.Store(true)
				} else {
					f.mu.Unlock()
				}
			}
			close(started)
			<-release
		})
	}

	errc := make(chan error, 1)
	go func() {
		got, _, err := s.Query("foo.service", time.Time{}, "")
		if err != nil {
			errc <- err
			return
		}
		if len(got) != 1 || got[0].Message != "buffered" {
			errc <- fmt.Errorf("query = %+v", got)
			return
		}
		errc <- nil
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("scan did not start")
	}

	done := make(chan struct{})
	var elapsed time.Duration
	go func() {
		start := time.Now()
		s.append(Entry{Timestamp: time.Now().UTC(), Unit: "foo.service", Message: "concurrent"})
		elapsed = time.Since(start)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		close(release)
		t.Fatal("append stalled while Query was scanning")
	}
	close(release)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if lockHeld.Load() {
		t.Fatal("Query held unitFile.mu during scan/decode")
	}
	if elapsed > 100*time.Millisecond {
		t.Fatalf("append held lock during scan: %v", elapsed)
	}
}

func TestFollowerOn10MiBDoesNotStallAppend(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	unit := "big.service"
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	line := journalJSONLine(old, unit, strings.Repeat("x", 200))
	payload := bytes.Repeat(line, (10<<20)/len(line)+1)
	if err := os.WriteFile(s.path(unit), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	s.append(Entry{Timestamp: old.Add(time.Hour), Unit: unit, Message: "tail"})
	s.syncUnit(unit)

	// Decode the fixture outside the production query deadline: under -race,
	// a busy runner can take over five seconds to seed this 10 MiB cursor.
	// The timed follower/append assertions below still use the public Query.
	seedCtx, cancelSeed := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelSeed()
	all, cur, _, err := s.QueryPageContext(seedCtx, unit, time.Time{}, "", 0)
	cancelSeed()
	if err != nil || len(all) < 2 || cur == "" {
		t.Fatalf("seed query = %d %q %v", len(all), cur, err)
	}

	start := time.Now()
	if _, _, err := s.Query(unit, time.Time{}, cur); err != nil {
		t.Fatal(err)
	}
	scanDur := time.Since(start)

	stop := make(chan struct{})
	var following atomic.Int32
	go func() {
		for {
			following.Add(1)
			if _, _, err := s.Query(unit, time.Time{}, cur); err != nil {
				return
			}
			select {
			case <-stop:
				return
			default:
			}
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for following.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if following.Load() == 0 {
		t.Fatal("follower never queried")
	}

	start = time.Now()
	s.append(Entry{Timestamp: time.Now().UTC(), Unit: unit, Message: "live"})
	elapsed := time.Since(start)
	close(stop)

	bound := 20 * time.Millisecond
	if runtime.GOOS == "windows" {
		// Flush of the 10 MiB current file on windows-latest is often
		// 30–40ms (a63a3b0: 33ms) and has been observed at ~106ms
		// under Actions load. Still far below a full-file scan.
		bound = 200 * time.Millisecond
	}
	if scanDur > 50*time.Millisecond {
		bound = scanDur / 5
	}
	if elapsed > bound {
		t.Fatalf("append blocked %v; scan is %v (want ≤ %v, a flush not a full read)", elapsed, scanDur, bound)
	}
}

func TestQuerySkipsArchivesAndIDsBeforeCursor(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	unit := "foo.service"
	old0 := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	var arch bytes.Buffer
	const nOld = 500
	for i := 0; i < nOld; i++ {
		arch.Write(journalJSONLine(old0.Add(time.Duration(i)*time.Millisecond), unit, "old"+strconv.Itoa(i)))
	}
	if err := os.WriteFile(s.path(unit)+".1", arch.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	new0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.append(Entry{Timestamp: new0, Unit: unit, Message: "new1"})
	s.append(Entry{Timestamp: new0.Add(time.Second), Unit: unit, Message: "new2"})
	s.syncUnit(unit)

	all, _, err := s.Query(unit, time.Time{}, "")
	if err != nil || len(all) != nOld+2 {
		t.Fatalf("all = %d, want %d (%v)", len(all), nOld+2, err)
	}
	// Cursor on the first current-file line: archive last precedes
	// cursorTS, so the rotated file must not be opened or hashed.
	cur := formatCursor(entryID(all[nOld]), 1)

	var ids atomic.Int32
	s.onEntryID = func() { ids.Add(1) }
	got, _, err := s.Query(unit, time.Time{}, cur)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Message != "new2" {
		t.Fatalf("after archive cursor = %+v", got)
	}
	if n := ids.Load(); n != 2 {
		t.Fatalf("entryID calls = %d, want 2 (archive skipped; cursor line + new2)", n)
	}

	// Current-file skip: timestamps before cursorTS must not compute entryID.
	mid := all[10]
	curMid := formatCursor(entryID(mid), 1)
	ids.Store(0)
	rest, _, err := s.Query(unit, time.Time{}, curMid)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != nOld-11+2 {
		t.Fatalf("rest after mid-archive cursor = %d, want %d", len(rest), nOld-11+2)
	}
	if n := ids.Load(); n != int32(len(rest)+1) {
		t.Fatalf("entryID calls = %d, want %d (cursor line + rest; not the 11 skipped)", n, len(rest)+1)
	}

	// Archive whose last line is at the cursor timestamp must still be scanned
	// (duplicate-id counting), so do not skip it.
	dupT := new0.Add(2 * time.Hour)
	var same bytes.Buffer
	same.Write(journalJSONLine(dupT, unit, "same"))
	same.Write(journalJSONLine(dupT, unit, "same"))
	if err := os.WriteFile(s.path(unit)+".2", same.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	s.append(Entry{Timestamp: dupT, Unit: unit, Message: "same"})
	s.syncUnit(unit)
	dups, dupCur, err := s.Query(unit, dupT, "")
	if err != nil || len(dups) != 3 {
		t.Fatalf("dups across archive = %d %v", len(dups), err)
	}
	after, _, err := s.Query(unit, dupT, dupCur)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Fatalf("duplicate cursor across archive replay = %+v", after)
	}
}

func TestQueryMidJournalCursorSkipsPriorEntryIDs(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const n = 200
	const mid = 80
	for i := 0; i < n; i++ {
		s.append(Entry{
			Timestamp: t0.Add(time.Duration(i) * time.Millisecond),
			Unit:      "foo.service",
			Message:   "line" + strconv.Itoa(i),
		})
	}
	s.syncUnit("foo.service")
	cur := formatCursor(entryID(Entry{
		Timestamp: t0.Add(time.Duration(mid) * time.Millisecond),
		Unit:      "foo.service",
		Message:   "line" + strconv.Itoa(mid),
	}), 1)

	var ids atomic.Int32
	s.onEntryID = func() { ids.Add(1) }
	got, _, err := s.Query("foo.service", time.Time{}, cur)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n-mid-1 || got[0].Message != "line"+strconv.Itoa(mid+1) {
		t.Fatalf("mid-journal rest = %d first=%v", len(got), got)
	}
	if g := ids.Load(); g != int32(len(got)+1) {
		t.Fatalf("entryID calls = %d, want %d (cursor + rest; not all %d prior lines)", g, len(got)+1, n)
	}
}

func TestQueryCursorOutOfOrderTimestamps(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	t1 := time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC)
	t2 := time.Date(2026, 1, 1, 0, 0, 2, 0, time.UTC)
	s.append(Entry{Timestamp: t2, Unit: "foo.service", Message: "late-ts-first"})
	s.append(Entry{Timestamp: t1, Unit: "foo.service", Message: "early-ts-second"})

	all, cur, err := s.Query("foo.service", time.Time{}, "")
	if err != nil || len(all) != 2 || cur == "" {
		t.Fatalf("all = %+v %q %v", all, cur, err)
	}
	more, _, err := s.Query("foo.service", time.Time{}, cur)
	if err != nil {
		t.Fatal(err)
	}
	if len(more) != 0 {
		t.Fatalf("out-of-order cursor replay = %+v", more)
	}
	first, cur1, err := s.Query("foo.service", time.Time{}, formatCursor(entryID(all[0]), 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].Message != "early-ts-second" {
		t.Fatalf("after first = %+v (cur1=%s)", first, cur1)
	}
}

func journalJSONLine(ts time.Time, unit, msg string) []byte {
	raw, err := json.Marshal(record{
		V:         FormatVersion,
		Timestamp: ts.UTC().Format(time.RFC3339Nano),
		Unit:      unit,
		Message:   msg,
	})
	if err != nil {
		panic(err)
	}
	return append(raw, '\n')
}

func TestOpenReuseNoFsyncPerLine(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	s.flushEvery = time.Hour
	var opens, syncs atomic.Int32
	s.onOpen = func() { opens.Add(1) }
	s.onSync = func() { syncs.Add(1) }

	const n = 25
	for i := 0; i < n; i++ {
		s.append(Entry{
			Timestamp: time.Now().UTC(),
			Unit:      "hook.service",
			Message:   "line " + strconv.Itoa(i),
		})
	}
	if g := opens.Load(); g != 1 {
		t.Fatalf("opens after %d lines = %d, want 1 (reuse)", n, g)
	}
	if g := syncs.Load(); g != 0 {
		t.Fatalf("Sync after %d lines = %d, want 0 (no fsync-per-line)", n, g)
	}

	s.Wait("hook.service")
	if g := syncs.Load(); g != 1 {
		t.Fatalf("Wait Sync = %d, want 1", g)
	}

	s.maxSize = 200
	for i := 0; i < 20; i++ {
		s.append(Entry{
			Timestamp: time.Now().UTC(),
			Unit:      "hook.service",
			Message:   strings.Repeat("y", 80) + strconv.Itoa(i),
		})
	}
	if g := opens.Load(); g < 2 {
		t.Fatalf("rotate should reopen, opens = %d", g)
	}
	if g := syncs.Load(); g < 2 {
		t.Fatalf("rotate should Sync, syncs = %d", g)
	}

	beforeClose := syncs.Load()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if g := syncs.Load(); g != beforeClose+1 {
		t.Fatalf("Close Sync = %d, want %d", g, beforeClose+1)
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

func TestWaitContextRetainsHungCapture(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	pr, pw := io.Pipe()
	s.Attach("hang.service", 1, NewInvocationID(), io.NopCloser(pr), strings.NewReader(""))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if s.WaitContext(ctx, "hang.service") {
		t.Fatal("WaitContext succeeded on a hung capture")
	}
	done := make(chan struct{})
	go func() {
		s.Wait("hang.service")
		close(done)
	}()
	for i := 0; i < 100; i++ {
		if s.WaitContext(ctx, "hang.service") {
			t.Fatal("retry forgot unfinished capture")
		}
	}
	select {
	case <-done:
		t.Fatal("unbounded wait forgot unfinished capture")
	default:
	}
	_ = pw.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("retained completion did not finish")
	}
}
