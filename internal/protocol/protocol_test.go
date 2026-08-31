package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestMethodsCoverCLIVerbs(t *testing.T) {
	t.Parallel()
	want := []string{
		"start", "stop", "restart", "status", "enable", "disable",
		"list-units", "list-timers", "logs", "daemon-reload", "verify",
	}
	if len(Methods) != len(want) {
		t.Fatalf("Methods = %v, want %v", Methods, want)
	}
	for i, name := range want {
		if Methods[i] != name {
			t.Fatalf("Methods[%d] = %q, want %q", i, Methods[i], name)
		}
		if !KnownMethod(name) {
			t.Fatalf("KnownMethod(%q) = false", name)
		}
	}
}

func TestListUnitsOverFakeListener(t *testing.T) {
	t.Parallel()
	h := HandlerFunc(func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		if method != MethodListUnits {
			t.Errorf("method = %q", method)
		}
		return ListUnitsResult{Units: []UnitStatus{{
			Name:        "foo.service",
			Kind:        "service",
			LoadState:   "loaded",
			ActiveState: "inactive",
		}}}, nil
	})
	client, stop := serveTest(t, h, AllowAdmin)
	defer stop()

	got, err := client.ListUnits(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Units) != 1 || got.Units[0].Name != "foo.service" {
		t.Fatalf("result = %+v", got)
	}
}

func TestNonAdminDenied(t *testing.T) {
	t.Parallel()
	called := false
	h := HandlerFunc(func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		called = true
		return ListUnitsResult{}, nil
	})
	client, stop := serveTest(t, h, DenyAll)
	defer stop()

	_, err := client.ListUnits(context.Background())
	if err == nil {
		t.Fatal("expected permission-denied")
	}
	pe, ok := err.(*Error)
	if !ok {
		t.Fatalf("err type %T: %v", err, err)
	}
	if pe.Code != CodePermissionDenied {
		t.Fatalf("code = %q, want %s", pe.Code, CodePermissionDenied)
	}
	if called {
		t.Fatal("handler must not run for non-admin")
	}
}

func TestProtocolVersionMismatch(t *testing.T) {
	t.Parallel()
	h := HandlerFunc(func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		t.Fatal("handler must not run")
		return nil, nil
	})
	conn, stop := serveConn(t, h, AllowAdmin)
	defer stop()

	raw := []byte(`{"protocol":"winunitd.control","version":99,"id":1,"method":"list-units"}` + "\n")
	if _, err := conn.Write(raw); err != nil {
		t.Fatal(err)
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil || resp.Error.Code != CodeProtocolMismatch {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Protocol != Name || resp.Version != Version {
		t.Fatalf("response must advertise current protocol: %+v", resp)
	}
	if resp.Result != nil {
		t.Fatalf("mismatch must not include a result: %s", resp.Result)
	}
}

func TestProtocolNameMismatch(t *testing.T) {
	t.Parallel()
	conn, stop := serveConn(t, HandlerFunc(func(context.Context, string, json.RawMessage) (any, error) {
		t.Fatal("handler must not run")
		return nil, nil
	}), AllowAdmin)
	defer stop()

	raw := []byte(`{"protocol":"other","version":1,"id":7,"method":"list-units"}` + "\n")
	if _, err := conn.Write(raw); err != nil {
		t.Fatal(err)
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil || resp.Error.Code != CodeProtocolMismatch {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.ID != 7 {
		t.Fatalf("id = %d", resp.ID)
	}
}

func TestUnknownMethod(t *testing.T) {
	t.Parallel()
	client, stop := serveTest(t, HandlerFunc(func(context.Context, string, json.RawMessage) (any, error) {
		t.Fatal("handler must not run")
		return nil, nil
	}), AllowAdmin)
	defer stop()

	err := client.Call(context.Background(), "not-a-verb", nil, nil)
	pe, ok := err.(*Error)
	if !ok || pe.Code != CodeMethodNotFound {
		t.Fatalf("err = %v", err)
	}
}

func TestAllMethodsRoundTrip(t *testing.T) {
	t.Parallel()
	seen := make(map[string]int)
	h := HandlerFunc(func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		seen[method]++
		switch method {
		case MethodListUnits:
			return ListUnitsResult{Units: []UnitStatus{{Name: "a.service", Kind: "service", LoadState: "loaded", ActiveState: "inactive"}}}, nil
		case MethodListTimers:
			return ListTimersResult{Timers: []TimerStatus{{Name: "a.timer", LoadState: "loaded", ActiveState: "inactive"}}}, nil
		case MethodStatus:
			return StatusResult{Machine: &MachineStatus{State: "running"}}, nil
		case MethodStart, MethodStop, MethodRestart:
			var p UnitParams
			if err := DecodeParams(params, &p); err != nil {
				return nil, err
			}
			return UnitResult{Unit: p.Unit, ActiveState: "inactive"}, nil
		case MethodEnable, MethodDisable:
			var p UnitParams
			if err := DecodeParams(params, &p); err != nil {
				return nil, err
			}
			return EnableResult{Unit: p.Unit, Enabled: method == MethodEnable}, nil
		case MethodLogs:
			var p LogsParams
			if err := DecodeParams(params, &p); err != nil {
				return nil, err
			}
			return LogsResult{Unit: p.Unit, Entries: []LogEntry{}}, nil
		case MethodDaemonReload:
			return DaemonReloadResult{Loaded: 1}, nil
		case MethodVerify:
			var p VerifyParams
			if err := DecodeParams(params, &p); err != nil {
				return nil, err
			}
			return VerifyResult{Name: p.Unit, OK: true}, nil
		default:
			return nil, ErrMethodNotFound(method)
		}
	})
	client, stop := serveTest(t, h, AllowAdmin)
	defer stop()
	ctx := context.Background()

	if _, err := client.ListUnits(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListTimers(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Status(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Start(ctx, "foo"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Stop(ctx, "foo"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Restart(ctx, "foo"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Enable(ctx, "foo"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Disable(ctx, "foo"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Logs(ctx, LogsParams{Unit: "foo"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.DaemonReload(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Verify(ctx, "foo.service"); err != nil {
		t.Fatal(err)
	}
	for _, name := range Methods {
		if seen[name] != 1 {
			t.Errorf("method %s called %d times", name, seen[name])
		}
	}
}

func TestCodecRoundTripRequest(t *testing.T) {
	t.Parallel()
	req := Request{
		Protocol: Name,
		Version:  Version,
		ID:       3,
		Method:   MethodListUnits,
		Params:   json.RawMessage(`{}`),
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "\n") {
		t.Fatalf("compact JSON must not contain newlines: %s", data)
	}
	var got Request
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Protocol != Name || got.Version != Version || got.Method != MethodListUnits {
		t.Fatalf("got %+v", got)
	}
}

func TestPeerAllowed(t *testing.T) {
	t.Parallel()
	if (Peer{}).Allowed() {
		t.Fatal("empty peer must be denied")
	}
	if !((Peer{Administrator: true}).Allowed()) {
		t.Fatal("administrator must be allowed")
	}
	if !((Peer{LocalSystem: true}).Allowed()) {
		t.Fatal("LocalSystem must be allowed")
	}
}

func TestControlPipeSDDL(t *testing.T) {
	t.Parallel()
	if DefaultPipeName != `\\.\pipe\winunitd\control` {
		t.Fatalf("pipe name = %q", DefaultPipeName)
	}
	if !strings.Contains(ControlPipeSDDL, "SY") {
		t.Fatal("SDDL must allow LocalSystem (SY)")
	}
	if !strings.Contains(ControlPipeSDDL, "BA") {
		t.Fatal("SDDL must allow Administrators (BA)")
	}
	if strings.Contains(ControlPipeSDDL, "WD") || strings.Contains(ControlPipeSDDL, "BU") {
		t.Fatal("SDDL must not allow Everyone/Users")
	}
}

func TestNilAuthorizerDenied(t *testing.T) {
	t.Parallel()
	client, stop := serveTest(t, HandlerFunc(func(context.Context, string, json.RawMessage) (any, error) {
		t.Fatal("handler must not run")
		return nil, nil
	}), nil)
	defer stop()
	err := client.Call(context.Background(), MethodListUnits, nil, nil)
	pe, ok := err.(*Error)
	if !ok || pe.Code != CodePermissionDenied {
		t.Fatalf("err = %v", err)
	}
}

func TestHandlerErrorCodePreserved(t *testing.T) {
	t.Parallel()
	h := HandlerFunc(func(context.Context, string, json.RawMessage) (any, error) {
		return nil, ErrNotFound("missing.service")
	})
	client, stop := serveTest(t, h, AllowAdmin)
	defer stop()
	_, err := client.Start(context.Background(), "missing")
	pe, ok := err.(*Error)
	if !ok || pe.Code != CodeNotFound {
		t.Fatalf("err = %v", err)
	}
}

func serveTest(t *testing.T, h Handler, auth Authorizer) (*Client, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() { errc <- Serve(ctx, lis, h, auth) }()

	var d net.Dialer
	conn, err := d.DialContext(ctx, lis.Addr().Network(), lis.Addr().String())
	if err != nil {
		cancel()
		lis.Close()
		t.Fatal(err)
	}
	client := NewClient(conn)
	stop := func() {
		cancel()
		_ = conn.Close()
		_ = lis.Close()
		select {
		case <-errc:
		case <-time.After(2 * time.Second):
		}
	}
	return client, stop
}

func serveConn(t *testing.T, h Handler, auth Authorizer) (net.Conn, func()) {
	t.Helper()
	c1, c2 := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ServeConn(ctx, c2, h, auth)
		_ = c2.Close()
	}()
	stop := func() {
		cancel()
		_ = c1.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
	return c1, stop
}

func TestServeConnEOF(t *testing.T) {
	t.Parallel()
	c1, c2 := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		ServeConn(context.Background(), c2, HandlerFunc(func(context.Context, string, json.RawMessage) (any, error) {
			return ListUnitsResult{}, nil
		}), AllowAdmin)
	}()
	_ = c1.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ServeConn did not return after close")
	}
}

func TestDecodeParamsEmpty(t *testing.T) {
	t.Parallel()
	var p UnitParams
	if err := DecodeParams(nil, &p); err != nil {
		t.Fatal(err)
	}
	if err := DecodeParams(json.RawMessage("null"), &p); err != nil {
		t.Fatal(err)
	}
	if err := DecodeParams(json.RawMessage(`{"unit":"foo"}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.Unit != "foo" {
		t.Fatalf("unit = %q", p.Unit)
	}
	err := DecodeParams(json.RawMessage(`{`), &p)
	if err == nil {
		t.Fatal("expected error")
	}
	if pe, ok := err.(*Error); !ok || pe.Code != CodeInvalidParams {
		t.Fatalf("err = %v", err)
	}
}

func TestErrorAs(t *testing.T) {
	t.Parallel()
	err := error(ErrPermissionDenied())
	var pe *Error
	if !errors.As(err, &pe) {
		t.Fatal("errors.As")
	}
	if pe.Code != CodePermissionDenied {
		t.Fatalf("code = %q", pe.Code)
	}
}

func TestUnexpectedEOF(t *testing.T) {
	t.Parallel()
	c1, c2 := net.Pipe()
	client := NewClient(c1)
	go func() {
		buf := make([]byte, 8)
		_, _ = c2.Read(buf)
		_ = c2.Close()
	}()
	err := client.Call(context.Background(), MethodListUnits, nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if err == nil || (!errors.Is(err, io.ErrClosedPipe) && !errors.Is(err, io.ErrUnexpectedEOF) && !strings.Contains(err.Error(), "closed") && !strings.Contains(err.Error(), "EOF")) {
		t.Fatalf("err = %v", err)
	}
	_ = c1.Close()
}
