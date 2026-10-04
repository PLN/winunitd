package protocol

import (
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

const (
	ordinaryMarker = 'x'
	ordinaryAnswer = 'a'
)

// awaitOrdinaryAfterRefusal reads accepted server connections until one is
// bound to the ordinary client (#283). The ordinary client sends a marker; the
// server connection that reads it answers, and only the ordinary client
// receiving that answer identifies it, so a marker on another connection
// cannot stand in for it. Every other accepted connection must be an empty
// refusal: no bytes, then io.EOF. A read error or timeout is unknown, not
// evidence of an empty connection.
func awaitOrdinaryAfterRefusal(conns <-chan net.Conn, ordinary net.Conn, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	sent := make(chan error, 1)
	go func() {
		if err := ordinary.SetWriteDeadline(deadline); err != nil {
			sent <- err
			return
		}
		_, err := ordinary.Write([]byte{ordinaryMarker})
		sent <- err
	}()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		var c net.Conn
		select {
		case got, ok := <-conns:
			if !ok {
				return errors.New("listener stopped accepting after the refused dial")
			}
			c = got
		case <-timer.C:
			return errors.New("no accepted connection answered the ordinary client")
		}
		bound, err := classifyAccepted(c, ordinary, deadline)
		_ = c.Close()
		if err != nil {
			return err
		}
		if bound {
			if err := <-sent; err != nil {
				return fmt.Errorf("ordinary client write: %w", err)
			}
			return nil
		}
	}
}

// classifyAccepted reports whether c is the ordinary client's connection, or
// returns nil for an empty refusal and an error for anything else.
func classifyAccepted(c, ordinary net.Conn, deadline time.Time) (bool, error) {
	if err := c.SetReadDeadline(deadline); err != nil {
		return false, fmt.Errorf("accepted connection read deadline: %w", err)
	}
	buf := make([]byte, 1)
	n, err := c.Read(buf)
	for n == 0 && err == nil { // bounded by the read deadline
		n, err = c.Read(buf)
	}
	switch {
	case n == 0 && errors.Is(err, io.EOF):
		return false, nil
	case n == 0:
		return false, fmt.Errorf("accepted connection read failed; emptiness unknown: %w", err)
	case buf[0] != ordinaryMarker:
		return false, fmt.Errorf("refused connection carried data %q", buf[:n])
	}
	got := make(chan error, 1)
	go func() {
		if err := ordinary.SetReadDeadline(deadline); err != nil {
			got <- err
			return
		}
		answer := make([]byte, 1)
		n, err := ordinary.Read(answer)
		if n == 1 && answer[0] == ordinaryAnswer {
			got <- nil
			return
		}
		got <- fmt.Errorf("read %q: %v", answer[:n], err)
	}()
	if err := c.SetWriteDeadline(deadline); err != nil {
		return false, fmt.Errorf("accepted connection write deadline: %w", err)
	}
	if _, err := c.Write([]byte{ordinaryAnswer}); err != nil {
		return false, fmt.Errorf("marker did not come from the ordinary client: answer failed: %w", err)
	}
	if err := <-got; err != nil {
		return false, fmt.Errorf("marker did not come from the ordinary client: it did not receive the answer: %w", err)
	}
	return true, nil
}

// failingConn reports a read or read-deadline failure with no bytes.
type failingConn struct {
	net.Conn
	readErr, deadlineErr error
}

func (c failingConn) Read([]byte) (int, error)        { return 0, c.readErr }
func (c failingConn) SetReadDeadline(time.Time) error { return c.deadlineErr }
func (c failingConn) Close() error                    { return nil }
