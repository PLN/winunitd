package protocol

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"time"
)

// Handler serves one control method. result is JSON-encoded on success.
type Handler interface {
	Handle(ctx context.Context, method string, params json.RawMessage) (result any, err error)
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(ctx context.Context, method string, params json.RawMessage) (any, error)

// Handle calls f.
func (f HandlerFunc) Handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	return f(ctx, method, params)
}

// Serve accepts connections on lis until ctx is cancelled or Accept fails.
func Serve(ctx context.Context, lis net.Listener, h Handler, auth Authorizer) error {
	return ServeWithLimits(ctx, lis, h, auth, ServerLimits{})
}

// ServeWithLimits bounds accepted connections and concurrent handler calls.
// A connection beyond the hard cap is closed without decoding; an authenticated
// request beyond its class budget receives a versioned busy response.
func ServeWithLimits(ctx context.Context, lis net.Listener, h Handler, auth Authorizer, limits ServerLimits) (resultErr error) {
	if h == nil {
		return ErrFailed("nil handler")
	}
	limits, err := limits.defaults()
	if err != nil {
		return err
	}
	h = &admittedHandler{next: h, requests: make(chan struct{}, limits.Requests), stops: make(chan struct{}, limits.Stops), diagnostics: make(chan struct{}, limits.Diagnostics)}
	connections := make(chan struct{}, limits.Connections)
	// Accepted connections belong to this serving lifetime, including when
	// Accept fails before the caller cancels its own context.
	ctx, cancel := context.WithCancel(ctx)
	closed := make(chan error, 1)
	defer func() {
		cancel()
		// Accept can return before Close finishes releasing the listening
		// handle. Keep that close owned until endpoint shutdown completes.
		if err := <-closed; err != nil && !errors.Is(err, net.ErrClosed) {
			resultErr = errors.Join(resultErr, err)
		}
	}()
	go func() {
		<-ctx.Done()
		closed <- lis.Close()
	}()
	for {
		conn, err := lis.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		select {
		case connections <- struct{}{}:
		default:
			_ = conn.Close()
			continue
		}
		go func() {
			defer func() { <-connections }()
			defer conn.Close()
			serveConnWithTimeouts(ctx, conn, h, auth, limits.ReadTimeout, limits.WriteTimeout)
		}()
	}
}

// ServeConn handles requests on one connection until it closes or ctx is done.
func ServeConn(ctx context.Context, conn io.ReadWriteCloser, h Handler, auth Authorizer) {
	serveConnWithTimeouts(ctx, conn, h, auth, 0, 0)
}

func serveConnWithTimeouts(ctx context.Context, conn io.ReadWriteCloser, h Handler, auth Authorizer, readTimeout, writeTimeout time.Duration) {
	if nc, ok := conn.(net.Conn); ok {
		stop := context.AfterFunc(ctx, func() { _ = nc.Close() })
		defer stop()
	}

	var netConn net.Conn
	if nc, ok := conn.(net.Conn); ok {
		netConn = nc
	}
	peer, err := authorize(auth, netConn)
	if err != nil {
		logAuthf("winunitd: authorizer: %v", err)
		peer = Peer{}
	}

	br := bufio.NewReader(conn)
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		var req Request
		if netConn != nil && readTimeout > 0 {
			if err := netConn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
				return
			}
		}
		if err := decodeMessage(br, &req); err != nil {
			if isDisconnect(err) {
				return
			}
			resp := newResponse(req.ID, nil, ErrInvalidRequest("malformed request"))
			if netConn != nil && writeTimeout > 0 {
				if err := netConn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
					return
				}
			}
			_ = encodeMessage(conn, resp)
			return
		}
		if netConn != nil && readTimeout > 0 {
			if err := netConn.SetReadDeadline(time.Time{}); err != nil {
				return
			}
		}
		resp := dispatch(ctx, h, peer, &req)
		if netConn != nil && writeTimeout > 0 {
			if err := netConn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
				return
			}
		}
		if err := encodeMessage(conn, resp); err != nil {
			return
		}
		if !peer.Allowed() {
			return
		}
	}
}

func dispatch(ctx context.Context, h Handler, peer Peer, req *Request) *Response {
	id := req.ID
	if req.Protocol != Name || req.Version != Version {
		return newResponse(id, nil, ErrProtocolMismatch(req.Protocol, req.Version))
	}
	if req.Method == "" {
		return newResponse(id, nil, ErrInvalidRequest("method required"))
	}
	if !peer.Allowed() {
		return newResponse(id, nil, ErrPermissionDenied())
	}
	if !KnownMethod(req.Method) {
		return newResponse(id, nil, ErrMethodNotFound(req.Method))
	}
	if AdministrativeMethod(req.Method) && !peer.CanLinger() {
		return newResponse(id, nil, ErrPermissionDenied())
	}
	result, err := h.Handle(ctx, req.Method, req.Params)
	return newResponse(id, result, err)
}

func isDisconnect(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed)
}
