package protocol

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// The ordinary-client check must bind the marker to the ordinary client and
// must not count a failed read as an empty refusal (#283).
func TestAwaitOrdinaryAfterRefusal(t *testing.T) {
	t.Parallel()
	// refused builds the server side of a refused connection whose client
	// sends payload, then closes it unless open is set.
	refused := func(payload []byte, open bool) func(*testing.T) net.Conn {
		return func(t *testing.T) net.Conn {
			server, client := connPair(t)
			go func() {
				if len(payload) > 0 {
					_, _ = client.Write(payload)
				}
				if !open {
					_ = client.Close()
				}
			}()
			return server
		}
	}
	failing := func(c failingConn) func(*testing.T) net.Conn {
		return func(*testing.T) net.Conn { return c }
	}
	cases := []struct {
		name    string
		refused func(*testing.T) net.Conn
		wait    time.Duration
		want    string
	}{
		{name: "accepted empty refusal", refused: refused(nil, false), wait: 5 * time.Second},
		{name: "discarded refusal", wait: 5 * time.Second},
		{name: "refused connection carrying the marker", refused: refused([]byte{ordinaryMarker}, false), wait: time.Second, want: "marker did not come from the ordinary client"},
		{name: "open refused connection carrying the marker", refused: refused([]byte{ordinaryMarker}, true), wait: time.Second, want: "marker did not come from the ordinary client"},
		{name: "refused connection carrying other data", refused: refused([]byte("r"), false), wait: 5 * time.Second, want: "carried data"},
		{name: "read error", refused: failing(failingConn{readErr: errors.New("pipe read failed")}), wait: 5 * time.Second, want: "emptiness unknown"},
		{name: "read deadline error", refused: failing(failingConn{deadlineErr: errors.New("deadline unsupported")}), wait: 5 * time.Second, want: "read deadline"},
		{name: "silent open refusal", refused: refused(nil, true), wait: 200 * time.Millisecond, want: "emptiness unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			conns := make(chan net.Conn, 2)
			if tc.refused != nil {
				conns <- tc.refused(t)
			}
			server, client := connPair(t)
			conns <- server
			err := awaitOrdinaryAfterRefusal(conns, client, tc.wait)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

// connPair returns both ends of a loopback TCP connection. Unlike net.Pipe it
// buffers writes and keeps deadlines settable after the peer closes, as a
// named pipe does.
func connPair(t *testing.T) (server, client net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			close(accepted)
			return
		}
		accepted <- c
	}()
	client, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	server, ok := <-accepted
	if !ok {
		_ = client.Close()
		t.Fatal("loopback accept failed")
	}
	t.Cleanup(func() {
		_ = server.Close()
		_ = client.Close()
	})
	return server, client
}
