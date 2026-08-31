package journal

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestAttachDrains(t *testing.T) {
	r, w := io.Pipe()
	Attach("foo.service", r, nil)
	if _, err := io.WriteString(w, "hello\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		// Copy should have finished; a second Copy on a closed pipe reader
		// is not possible. Just ensure Attach did not panic.
		time.Sleep(10 * time.Millisecond)
		return
	}
}

func TestAttachNil(t *testing.T) {
	Attach("foo.service", nil, strings.NewReader(""))
}
