package journal

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestAttachDrainsWithoutStore(t *testing.T) {
	t.Parallel()
	done := make(chan struct{})
	r := &eofNotify{r: strings.NewReader("hello\n"), done: done}
	(*Store)(nil).Attach("foo.service", 0, "", r, nil)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("nil Store Attach did not drain the pipe")
	}
}

func TestAttachNilWithoutStore(t *testing.T) {
	t.Parallel()
	(*Store)(nil).Attach("foo.service", 0, "", nil, strings.NewReader(""))
}

type eofNotify struct {
	r    io.Reader
	done chan struct{}
}

func (n *eofNotify) Read(p []byte) (int, error) {
	nr, err := n.r.Read(p)
	if err == io.EOF {
		select {
		case <-n.done:
		default:
			close(n.done)
		}
	}
	return nr, err
}
