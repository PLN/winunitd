package journal

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestAttachDrainsWithoutStore(t *testing.T) {
	r, w := io.Pipe()
	(*Store)(nil).Attach("foo.service", 0, "", r, nil)
	if _, err := io.WriteString(w, "hello\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		return
	}
}

func TestAttachNilWithoutStore(t *testing.T) {
	(*Store)(nil).Attach("foo.service", 0, "", nil, strings.NewReader(""))
}
