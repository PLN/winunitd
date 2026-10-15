package client

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/PLN/winunitd/internal/protocol"
)

// pipeClient serves h on one end of an in-memory connection.
func pipeClient(t *testing.T, h protocol.HandlerFunc) *Client {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	conn, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		protocol.ServeConn(ctx, server, h, protocol.AllowLocalSystem)
	}()
	c := newClient(conn)
	t.Cleanup(func() {
		_ = c.Close()
		cancel()
		<-done
	})
	return c
}

func capabilities(methods ...string) protocol.CapabilitiesResult {
	return protocol.CapabilitiesResult{
		Product:                      "winunitd",
		Version:                      "0.4.0",
		Go:                           "go1.27.2",
		Platform:                     "windows/amd64",
		Scope:                        "system",
		Protocol:                     protocol.ProtocolCapabilities{Name: protocol.Name, Version: protocol.Version, Methods: methods},
		FormatVersions:               []int{1, 2},
		Features:                     []string{},
		JobLimits:                    []string{},
		UserManagerModes:             []string{},
		ExperimentalUserManagerModes: []string{},
		Directives:                   map[string][]string{},
	}
}

func TestTypedCallsAndRefusals(t *testing.T) {
	c := pipeClient(t, func(_ context.Context, method string, raw json.RawMessage) (any, error) {
		switch method {
		case MethodUnitOfProcess:
			var p UnitOfProcessParams
			if err := protocol.DecodeParams(raw, &p); err != nil {
				return nil, err
			}
			if p.PID != 4321 {
				return nil, &protocol.Error{Code: CodeNotFound, Message: "process belongs to no unit of this manager"}
			}
			return UnitOfProcessResult{PID: p.PID, Scope: "system", Unit: "app.service", Main: true}, nil
		case MethodStatus:
			return StatusResult{Unit: &UnitStatus{Name: "app.service", ActiveState: "active"}}, nil
		}
		return nil, protocol.ErrMethodNotFound(method)
	})
	ctx := context.Background()
	var refusal *Error
	if _, err := c.UnitOfProcess(ctx, 77); !errors.As(err, &refusal) || refusal.Code != CodeNotFound {
		t.Fatalf("outside every unit: %v", err)
	}
	got, err := c.UnitOfProcess(ctx, 4321)
	if err != nil || got.Unit != "app.service" || !got.Main {
		t.Fatalf("after a refusal the client stays open: %+v %v", got, err)
	}
	st, err := c.Status(ctx, "app")
	if err != nil || st.Unit == nil || st.Unit.ActiveState != "active" {
		t.Fatalf("status: %+v %v", st, err)
	}
	if err := c.Call(ctx, MethodSnapshot, nil, nil); !errors.As(err, &refusal) || refusal.Code != CodeMethodNotFound {
		t.Fatalf("unknown method: %v", err)
	}
}

func TestSupportsFollowsCapabilities(t *testing.T) {
	c := pipeClient(t, func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		if method != MethodCapabilities {
			return nil, protocol.ErrMethodNotFound(method)
		}
		return capabilities(MethodStatus, MethodCapabilities), nil
	})
	if c.Supports(MethodStatus) {
		t.Fatal("Supports before Capabilities")
	}
	got, err := c.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got.Protocol.Methods[0] = MethodUnitOfProcess // the caller's copy is its own
	if !c.Supports(MethodStatus) || !c.Supports(MethodCapabilities) || c.Supports(MethodUnitOfProcess) {
		t.Fatal("Supports does not match the reply")
	}
}

// blockingClient's handler signals entered and blocks until release closes.
func blockingClient(t *testing.T) (c *Client, entered chan string, release chan struct{}) {
	t.Helper()
	entered = make(chan string, 4)
	release = make(chan struct{})
	t.Cleanup(func() { close(release) })
	c = pipeClient(t, func(ctx context.Context, method string, _ json.RawMessage) (any, error) {
		entered <- method
		select {
		case <-release:
		case <-ctx.Done():
		}
		return StatusResult{Machine: &MachineStatus{State: "running"}}, nil
	})
	return c, entered, release
}

func bounded(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestCancellationClosesClient(t *testing.T) {
	c, entered, _ := blockingClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-entered
		cancel()
	}()
	if _, err := c.Start(ctx, "app"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled start: %v", err)
	}
	if _, err := c.Status(bounded(t), ""); !errors.Is(err, ErrClosed) {
		t.Fatalf("after an interrupted request: %v", err)
	}
}

func TestDeadlineInterruptsRequest(t *testing.T) {
	c, _, _ := blockingClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.Start(ctx, "app"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("start past its deadline: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("deadline did not interrupt the request")
	}
}

func TestQueuedRequestHonoursItsContext(t *testing.T) {
	c, entered, release := blockingClient(t)
	first := make(chan error, 1)
	go func() {
		_, err := c.Status(bounded(t), "")
		first <- err
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.Status(ctx, ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued request: %v", err)
	}
	select {
	case err := <-first:
		t.Fatalf("active request ended early: %v", err)
	default:
	}
	release <- struct{}{}
	if err := <-first; err != nil {
		t.Fatalf("active request after the queued one gave up: %v", err)
	}
	go func() {
		<-entered
		release <- struct{}{}
	}()
	if _, err := c.Status(bounded(t), ""); err != nil {
		t.Fatalf("client after both: %v", err)
	}
}

func TestCloseInterruptsRequest(t *testing.T) {
	c, entered, _ := blockingClient(t)
	go func() {
		<-entered
		_ = c.Close()
	}()
	if _, err := c.Status(context.Background(), ""); !errors.Is(err, ErrClosed) {
		t.Fatalf("request during Close: %v", err)
	}
}

// A cancellation that loses the race to the reply must not leave an expired
// deadline behind for the next request.
func TestLateCancellationLeavesClientUsable(t *testing.T) {
	c := pipeClient(t, func(context.Context, string, json.RawMessage) (any, error) {
		return StatusResult{Machine: &MachineStatus{State: "running"}}, nil
	})
	for i := 0; i < 200; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		go cancel()
		if _, err := c.Status(ctx, ""); err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("round %d: %v", i, err)
		}
		if c.isClosed() {
			// The cancellation interrupted that request; dialing again is the contract.
			c = pipeClient(t, func(context.Context, string, json.RawMessage) (any, error) {
				return StatusResult{Machine: &MachineStatus{State: "running"}}, nil
			})
			continue
		}
		if _, err := c.Status(bounded(t), ""); err != nil {
			t.Fatalf("round %d, request after a late cancellation: %v", i, err)
		}
	}
}

func TestClosedClient(t *testing.T) {
	c := pipeClient(t, func(context.Context, string, json.RawMessage) (any, error) { return nil, nil })
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(context.Background(), MethodStatus, nil, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("call after close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}
