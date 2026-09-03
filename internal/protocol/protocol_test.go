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
		"enable-linger", "disable-linger",
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
			return LogsResult{Unit: p.Unit, Entries: []LogEntry{{
				Unit:    p.Unit,
				Stream:  "stdout",
				Message: "hello",
				PID:     1,
			}}}, nil
		case MethodDaemonReload:
			return DaemonReloadResult{Loaded: 1}, nil
		case MethodVerify:
			var p VerifyParams
			if err := DecodeParams(params, &p); err != nil {
				return nil, err
			}
			return VerifyResult{Name: p.Unit, OK: true}, nil
		case MethodEnableLinger, MethodDisableLinger:
			var p LingerParams
			if err := DecodeParams(params, &p); err != nil {
				return nil, err
			}
			return LingerResult{SID: "S-1-5-21-1-2-3-1001", User: p.User, Lingering: method == MethodEnableLinger}, nil
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
	gotLogs, err := client.Logs(ctx, LogsParams{Unit: "foo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(gotLogs.Entries) != 1 || gotLogs.Entries[0].Message != "hello" || gotLogs.Entries[0].Stream != "stdout" {
		t.Fatalf("logs = %+v", gotLogs)
	}
	if _, err := client.DaemonReload(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Verify(ctx, "foo.service"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.EnableLinger(ctx, "ferdinand"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.DisableLinger(ctx, "ferdinand"); err != nil {
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
	if !((Peer{Owner: true}).Allowed()) {
		t.Fatal("user-pipe owner must be allowed")
	}
	if !(Peer{Administrator: true}).CanLinger() {
		t.Fatal("administrator must be able to linger")
	}
	if !(Peer{LocalSystem: true}).CanLinger() {
		t.Fatal("LocalSystem must be able to linger")
	}
	if (Peer{Owner: true}).CanLinger() {
		t.Fatal("owner-only must fail closed for linger")
	}
	if (Peer{}).CanLinger() {
		t.Fatal("empty peer must fail closed for linger")
	}
	if (Peer{SID: "S-1-5-21-1-2-3-1001"}).Allowed() {
		t.Fatal("SID alone must not grant access")
	}
	if (Peer{SID: "S-1-5-21-1-2-3-1001"}).CanLinger() {
		t.Fatal("SID alone must not grant linger")
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

func TestUserPipeNameAndSDDL(t *testing.T) {
	t.Parallel()
	sid := "S-1-5-21-3623811015-3361044348-30300820-1013"
	name := UserPipeName(sid)
	want := `\\.\pipe\winunitd\user\` + sid + `\control`
	if name != want {
		t.Fatalf("UserPipeName = %q, want %q", name, want)
	}
	sddl, err := UserPipeSDDL(sid)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sddl, sid) {
		t.Fatalf("SDDL missing user SID: %s", sddl)
	}
	if !strings.Contains(sddl, "SY") || !strings.Contains(sddl, "BA") {
		t.Fatalf("SDDL must allow LocalSystem and Administrators: %s", sddl)
	}
	if strings.Contains(sddl, "WD") || strings.Contains(sddl, "BU") {
		t.Fatal("SDDL must not allow Everyone/Users")
	}
	if _, err := UserPipeSDDL("not-a-sid"); err == nil {
		t.Fatal("invalid SID must be rejected")
	}
	if _, err := UserPipeSDDL("S-1-5-21-1)(A;;GA;;;WD"); err == nil {
		t.Fatal("SDDL injection in SID must be rejected")
	}
	if !ValidSID(sid) || ValidSID("") || ValidSID("S-") || ValidSID("Everyone") {
		t.Fatal("ValidSID")
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

func TestNonAdminEnableLingerFailsClosed(t *testing.T) {
	t.Parallel()
	called := false
	h := HandlerFunc(func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		called = true
		return LingerResult{Lingering: true}, nil
	})
	client, stop := serveTest(t, h, AllowOwner)
	defer stop()
	_, err := client.EnableLinger(context.Background(), "ferdinand")
	pe, ok := err.(*Error)
	if !ok || pe.Code != CodePermissionDenied {
		t.Fatalf("err = %v", err)
	}
	if called {
		t.Fatal("handler must not run for non-admin linger")
	}

	client2, stop2 := serveTest(t, h, DenyAll)
	defer stop2()
	_, err = client2.EnableLinger(context.Background(), "ferdinand")
	pe, ok = err.(*Error)
	if !ok || pe.Code != CodePermissionDenied {
		t.Fatalf("deny-all err = %v", err)
	}
}

func TestDefaultAuthorizerDoesNotStampAdministrator(t *testing.T) {
	t.Parallel()
	p, err := DefaultAuthorizer()(nil)
	if p.Administrator || p.LocalSystem || p.Owner {
		t.Fatalf("DefaultAuthorizer(nil) stamped privileges: %+v (err=%v)", p, err)
	}
	if p.Allowed() || p.CanLinger() {
		t.Fatalf("DefaultAuthorizer(nil) must fail closed: %+v", p)
	}

	called := false
	h := HandlerFunc(func(context.Context, string, json.RawMessage) (any, error) {
		called = true
		return ListUnitsResult{}, nil
	})
	client, stop := serveTest(t, h, DefaultAuthorizer())
	defer stop()
	_, callErr := client.ListUnits(context.Background())
	pe, ok := callErr.(*Error)
	if !ok || pe.Code != CodePermissionDenied {
		t.Fatalf("DefaultAuthorizer on a fake listener must deny: %v", callErr)
	}
	if called {
		t.Fatal("handler must not run")
	}
}

func TestAuthorizerGatesEveryKnownMethod(t *testing.T) {
	t.Parallel()
	for _, name := range Methods {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			called := false
			h := HandlerFunc(func(context.Context, string, json.RawMessage) (any, error) {
				called = true
				return struct{}{}, nil
			})
			client, stop := serveTest(t, h, DenyAll)
			defer stop()
			err := client.Call(context.Background(), name, map[string]string{}, nil)
			pe, ok := err.(*Error)
			if !ok || pe.Code != CodePermissionDenied {
				t.Fatalf("err = %v", err)
			}
			if called {
				t.Fatal("handler must not run when Authorizer denies")
			}
		})
	}
}

func TestOwnerMayUseAPIButNotLinger(t *testing.T) {
	t.Parallel()
	called := ""
	h := HandlerFunc(func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		called = method
		if method == MethodListUnits {
			return ListUnitsResult{}, nil
		}
		return LingerResult{}, nil
	})
	client, stop := serveTest(t, h, AllowOwner)
	defer stop()
	if _, err := client.ListUnits(context.Background()); err != nil {
		t.Fatalf("owner list-units: %v", err)
	}
	if called != MethodListUnits {
		t.Fatalf("called = %q", called)
	}
	_, err := client.EnableLinger(context.Background(), "ferdinand")
	pe, ok := err.(*Error)
	if !ok || pe.Code != CodePermissionDenied {
		t.Fatalf("owner linger err = %v", err)
	}
	if called != MethodListUnits {
		t.Fatal("handler must not run for owner linger")
	}
}

func TestMalformedJSONInvalidRequestThenClose(t *testing.T) {
	t.Parallel()
	called := false
	h := HandlerFunc(func(context.Context, string, json.RawMessage) (any, error) {
		called = true
		return ListUnitsResult{}, nil
	})
	conn, stop := serveConn(t, h, AllowAdmin)
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	if _, err := conn.Write([]byte("{not-json\n")); err != nil {
		t.Fatal(err)
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil || resp.Error.Code != CodeInvalidRequest {
		t.Fatalf("resp = %+v", resp)
	}
	if called {
		t.Fatal("handler must not run for malformed JSON")
	}
	buf := make([]byte, 16)
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("connection must close after malformed JSON")
	}
}

func TestLogsParamsFollowSinceOnWire(t *testing.T) {
	t.Parallel()
	p := LogsParams{Unit: "foo.service", Follow: true, Since: "1h"}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, `"follow":true`) {
		t.Fatalf("Follow must be on the wire, not ignored: %s", s)
	}
	if !strings.Contains(s, `"since":"1h"`) {
		t.Fatalf("Since must be on the wire, not ignored: %s", s)
	}
	var got LogsParams
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Follow || got.Since != "1h" || got.Unit != "foo.service" {
		t.Fatalf("round-trip = %+v", got)
	}
}

func TestAdminEnableLingerAllowed(t *testing.T) {
	t.Parallel()
	h := HandlerFunc(func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		if method != MethodEnableLinger {
			t.Fatalf("method = %s", method)
		}
		var p LingerParams
		if err := DecodeParams(params, &p); err != nil {
			return nil, err
		}
		if p.User != "ferdinand" {
			t.Fatalf("user = %q", p.User)
		}
		return LingerResult{SID: "S-1-5-21-1-2-3-1001", User: p.User, Lingering: true}, nil
	})
	client, stop := serveTest(t, h, AllowAdmin)
	defer stop()
	got, err := client.EnableLinger(context.Background(), "ferdinand")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Lingering || got.SID == "" {
		t.Fatalf("result = %+v", got)
	}

	clientSys, stopSys := serveTest(t, h, AllowLocalSystem)
	defer stopSys()
	if _, err := clientSys.EnableLinger(context.Background(), "ferdinand"); err != nil {
		t.Fatalf("LocalSystem linger: %v", err)
	}
}
