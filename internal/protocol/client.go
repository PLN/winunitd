package protocol

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// Client is a sequential JSON-RPC client for the control protocol.
type Client struct {
	rw   io.ReadWriter
	br   *bufio.Reader
	mu   sync.Mutex
	next atomic.Uint64
}

// NewClient wraps rw. The caller owns closing the underlying connection.
func NewClient(rw io.ReadWriter) *Client {
	return &Client{rw: rw, br: bufio.NewReader(rw)}
}

func (c *Client) Operation(ctx context.Context, id string) (*OperationResult, error) {
	var out OperationResult
	if err := c.Call(ctx, MethodOperation, OperationParams{ID: id}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CancelOperation requests cancellation; the returned state can still be running.
func (c *Client) CancelOperation(ctx context.Context, id string) (*OperationResult, error) {
	var out OperationResult
	if err := c.Call(ctx, MethodCancelOperation, OperationParams{ID: id}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Call sends a versioned request and decodes the result into out (optional).
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id := c.next.Add(1)
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		raw = b
	}
	req := Request{
		Protocol: Name,
		Version:  Version,
		ID:       id,
		Method:   method,
		Params:   raw,
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if err := encodeMessage(c.rw, req); err != nil {
		return fmt.Errorf("write request: %w", err)
	}
	var resp Response
	if err := decodeMessage(c.br, &resp); err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.ID != id {
		return fmt.Errorf("response id %d does not match request %d", resp.ID, id)
	}
	if resp.Error != nil {
		return resp.Error
	}
	if out == nil || len(resp.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(resp.Result, out); err != nil {
		return fmt.Errorf("decode result: %w", err)
	}
	return nil
}

func (c *Client) ListUnits(ctx context.Context) (*ListUnitsResult, error) {
	var out ListUnitsResult
	if err := c.Call(ctx, MethodListUnits, ListUnitsParams{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ListTimers(ctx context.Context) (*ListTimersResult, error) {
	var out ListTimersResult
	if err := c.Call(ctx, MethodListTimers, ListTimersParams{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Status(ctx context.Context, unit string) (*StatusResult, error) {
	var out StatusResult
	if err := c.Call(ctx, MethodStatus, StatusParams{Unit: unit}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Start(ctx context.Context, unit string) (*UnitResult, error) {
	return c.unitVerb(ctx, MethodStart, unit)
}

func (c *Client) Stop(ctx context.Context, unit string) (*UnitResult, error) {
	return c.unitVerb(ctx, MethodStop, unit)
}

func (c *Client) Restart(ctx context.Context, unit string) (*UnitResult, error) {
	return c.unitVerb(ctx, MethodRestart, unit)
}

func (c *Client) unitVerb(ctx context.Context, method, unit string) (*UnitResult, error) {
	var out UnitResult
	if err := c.Call(ctx, method, UnitParams{Unit: unit}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Enable(ctx context.Context, unit string) (*EnableResult, error) {
	var out EnableResult
	if err := c.Call(ctx, MethodEnable, UnitParams{Unit: unit}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Disable(ctx context.Context, unit string) (*EnableResult, error) {
	var out EnableResult
	if err := c.Call(ctx, MethodDisable, UnitParams{Unit: unit}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Logs fetches a journal snapshot. Pass Since / Follow / Cursor as on
// LogsParams (DESIGN.md §22); invalid Since is invalid-params.
func (c *Client) Logs(ctx context.Context, p LogsParams) (*LogsResult, error) {
	var out LogsResult
	if err := c.Call(ctx, MethodLogs, p, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DaemonReload(ctx context.Context) (*DaemonReloadResult, error) {
	var out DaemonReloadResult
	if err := c.Call(ctx, MethodDaemonReload, DaemonReloadParams{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Verify(ctx context.Context, unit string) (*VerifyResult, error) {
	var out VerifyResult
	if err := c.Call(ctx, MethodVerify, VerifyParams{Unit: unit}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) EnableLinger(ctx context.Context, user string) (*LingerResult, error) {
	return c.lingerVerb(ctx, MethodEnableLinger, user)
}

func (c *Client) DisableLinger(ctx context.Context, user string) (*LingerResult, error) {
	return c.lingerVerb(ctx, MethodDisableLinger, user)
}

func (c *Client) lingerVerb(ctx context.Context, method, user string) (*LingerResult, error) {
	var out LingerResult
	if err := c.Call(ctx, method, LingerParams{User: user}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
