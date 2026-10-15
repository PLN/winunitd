package client

import (
	"context"
	"errors"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/PLN/winunitd/internal/protocol"
)

// ErrClosed is returned by requests on a closed Client.
var ErrClosed = errors.New("winunitd client is closed")

// Client is one connection to a winunitd manager. It is safe for concurrent
// use; requests take turns, and a request waiting for its turn honours its
// context.
type Client struct {
	turn    chan struct{} // held by the request using the connection
	conn    net.Conn
	rpc     *protocol.Client
	mu      sync.Mutex // closed and methods
	closed  bool
	methods []string // from the last capabilities reply; nil before one
}

// Dial connects to the system manager.
func Dial(ctx context.Context) (*Client, error) {
	conn, err := protocol.DialDefault(ctx)
	if err != nil {
		return nil, err
	}
	return newClient(conn), nil
}

// DialUser connects to the user manager of the user with this SID. That
// manager must be running: it starts at sign-in for admitted users, and at
// boot or on EnableLinger for lingering users.
func DialUser(ctx context.Context, sid string) (*Client, error) {
	conn, err := protocol.DialUser(ctx, sid)
	if err != nil {
		return nil, err
	}
	return newClient(conn), nil
}

// DialCurrentUser connects to the user manager of the calling process's user.
func DialCurrentUser(ctx context.Context) (*Client, error) {
	sid, err := protocol.CurrentUserSID()
	if err != nil {
		return nil, err
	}
	return DialUser(ctx, sid)
}

func newClient(conn net.Conn) *Client {
	return &Client{turn: make(chan struct{}, 1), conn: conn, rpc: protocol.NewClient(conn)}
}

// Close closes the connection. A request in progress fails with ErrClosed.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.conn.Close()
}

func (c *Client) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// do runs one request with ctx bounding the connection I/O. A failure other
// than a manager refusal leaves the stream position unknown, so it closes the
// connection.
func (c *Client) do(ctx context.Context, request func() error) error {
	select {
	case c.turn <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-c.turn }()
	if c.isClosed() {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.conn.SetDeadline(deadline)
	}
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = c.conn.SetDeadline(time.Unix(1, 0))
		close(interrupted)
	})
	err := request()
	if !stop() {
		<-interrupted // never leave an expired deadline for the next request
	}
	_ = c.conn.SetDeadline(time.Time{})
	var refusal *Error
	if err == nil || errors.As(err, &refusal) {
		return err
	}
	if c.isClosed() {
		return errors.Join(ErrClosed, err)
	}
	_ = c.Close()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return errors.Join(ctxErr, err)
	}
	// The connection deadline can fire before the context's own timer.
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return errors.Join(context.DeadlineExceeded, err)
	}
	return err
}

// Call sends one request and decodes its result into result, which may be
// nil. Use it for methods without a typed helper.
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	return c.do(ctx, func() error { return c.rpc.Call(ctx, method, params, result) })
}

func run[T any](c *Client, ctx context.Context, request func(*protocol.Client) (*T, error)) (*T, error) {
	var out *T
	err := c.do(ctx, func() error {
		var err error
		out, err = request(c.rpc)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Capabilities asks the manager for its build, methods and features, and
// remembers the method list for Supports. A manager older than this query
// answers CodeMethodNotFound. An absent or invalid reply is an error.
func (c *Client) Capabilities(ctx context.Context) (*CapabilitiesResult, error) {
	got, err := run(c, ctx, func(r *protocol.Client) (*CapabilitiesResult, error) { return r.Capabilities(ctx) })
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.methods = slices.Clone(got.Protocol.Methods)
	c.mu.Unlock()
	return got, nil
}

// Supports reports whether the last successful Capabilities reply on this
// Client listed method. Before one, it reports false.
func (c *Client) Supports(method string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Contains(c.methods, method)
}

// Status returns one unit's status, or the manager's when unit is empty. A
// unit that is not loaded is a CodeNotFound error.
func (c *Client) Status(ctx context.Context, unit string) (*StatusResult, error) {
	return run(c, ctx, func(r *protocol.Client) (*StatusResult, error) { return r.Status(ctx, unit) })
}

// ListUnits returns every loaded unit.
func (c *Client) ListUnits(ctx context.Context) (*ListUnitsResult, error) {
	return run(c, ctx, func(r *protocol.Client) (*ListUnitsResult, error) { return r.ListUnits(ctx) })
}

// Start starts a unit and its requirements.
func (c *Client) Start(ctx context.Context, unit string) (*UnitResult, error) {
	return run(c, ctx, func(r *protocol.Client) (*UnitResult, error) { return r.Start(ctx, unit) })
}

// Stop stops a unit and the units that require it.
func (c *Client) Stop(ctx context.Context, unit string) (*UnitResult, error) {
	return run(c, ctx, func(r *protocol.Client) (*UnitResult, error) { return r.Stop(ctx, unit) })
}

// Restart stops and starts a unit.
func (c *Client) Restart(ctx context.Context, unit string) (*UnitResult, error) {
	return run(c, ctx, func(r *protocol.Client) (*UnitResult, error) { return r.Restart(ctx, unit) })
}

// Enable links a unit into the targets named by its WantedBy=. It does not
// start the unit.
func (c *Client) Enable(ctx context.Context, unit string) (*EnableResult, error) {
	return run(c, ctx, func(r *protocol.Client) (*EnableResult, error) { return r.Enable(ctx, unit) })
}

// Disable removes a unit's target links. It does not stop the unit.
func (c *Client) Disable(ctx context.Context, unit string) (*EnableResult, error) {
	return run(c, ctx, func(r *protocol.Client) (*EnableResult, error) { return r.Disable(ctx, unit) })
}

// DaemonReload reloads every unit file. The manager accepts the whole set or
// keeps the previous one; check the result's Errors and Cycle.
func (c *Client) DaemonReload(ctx context.Context) (*DaemonReloadResult, error) {
	return run(c, ctx, func(r *protocol.Client) (*DaemonReloadResult, error) { return r.DaemonReload(ctx) })
}

// Operation returns a retained start, stop or restart outcome.
func (c *Client) Operation(ctx context.Context, id string) (*OperationResult, error) {
	return run(c, ctx, func(r *protocol.Client) (*OperationResult, error) { return r.Operation(ctx, id) })
}

// UnitOfProcess returns the unit whose job holds pid. A process outside every
// unit of this manager is a CodeNotFound error.
func (c *Client) UnitOfProcess(ctx context.Context, pid int) (*UnitOfProcessResult, error) {
	return run(c, ctx, func(r *protocol.Client) (*UnitOfProcessResult, error) { return r.UnitOfProcess(ctx, pid) })
}

// EnableLinger starts a user manager for user now and at every boot, without
// a sign-in. user is DOMAIN\account or a SID. Only the system manager serves
// it, for administrators and LocalSystem.
func (c *Client) EnableLinger(ctx context.Context, user string) (*LingerResult, error) {
	return run(c, ctx, func(r *protocol.Client) (*LingerResult, error) { return r.EnableLinger(ctx, user) })
}

// DisableLinger revokes linger. The user manager stops unless an admitted
// interactive session remains.
func (c *Client) DisableLinger(ctx context.Context, user string) (*LingerResult, error) {
	return run(c, ctx, func(r *protocol.Client) (*LingerResult, error) { return r.DisableLinger(ctx, user) })
}
