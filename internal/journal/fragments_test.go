package journal

import (
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestCaptureFragmentsBoundsAndUnicode(t *testing.T) {
	for _, text := range []string{
		strings.Repeat("x", MaxCaptureFragment*3+17),
		strings.Repeat("x", MaxCaptureFragment-1) + strings.Repeat("界", MaxCaptureFragment),
		strings.Repeat("x", MaxCaptureFragment),
	} {
		var got strings.Builder
		parts := 0
		captureFragments(strings.NewReader(text+"\n"), func(msg string, continuation, partial bool) {
			if len(msg) > MaxCaptureFragment || !utf8.ValidString(msg) {
				t.Fatal("fragment exceeded limit or split UTF-8")
			}
			if continuation != (parts > 0) {
				t.Fatal("incorrect continuation metadata")
			}
			got.WriteString(msg)
			parts++
			if partial != (got.Len() < len(text)) {
				t.Fatal("incorrect partial metadata")
			}
		})
		if got.String() != text {
			t.Fatal("fragment reconstruction lost output")
		}
	}
}

func TestCaptureEmitsBeforeNewlineOrEOF(t *testing.T) {
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()
	seen := make(chan int, 2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		captureFragments(r, func(msg string, _, _ bool) { seen <- len(msg) })
	}()
	go func() { _, _ = io.WriteString(w, strings.Repeat("x", MaxCaptureFragment+1)) }()
	select {
	case n := <-seen:
		if n != MaxCaptureFragment {
			t.Fatalf("first fragment size = %d", n)
		}
	case <-time.After(time.Second):
		t.Fatal("capture waited for newline or EOF")
	}
	_ = w.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("capture did not finish")
	}
}

func TestCaptureFragmentJournalRoundTrip(t *testing.T) {
	s := testStore(t)
	text := strings.Repeat("x", MaxCaptureFragment*2+1)
	s.Attach("worker.service", 42, "example-invocation", strings.NewReader(text+"\nend"), nil)
	s.Wait("worker.service")
	entries, err := s.Read("worker.service")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("entries = %d, want 4", len(entries))
	}
	var got strings.Builder
	for i, e := range entries[:3] {
		if e.Continuation != (i > 0) || e.Partial != (i < 2) {
			t.Fatalf("fragment %d metadata lost", i)
		}
		if e.InvocationID != "example-invocation" || e.Stream != "stdout" {
			t.Fatal("stream identity lost")
		}
		got.WriteString(e.Message)
	}
	if got.String() != text {
		t.Fatal("stored fragments lost output")
	}
	last := entries[3]
	if last.Message != "end" || last.Continuation || !last.Partial {
		t.Fatal("unterminated final line metadata lost")
	}
}
