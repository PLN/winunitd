package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestImpersonatedIdentityFailureCannotUsePIDFallback(t *testing.T) {
	cause := errors.New("token query or revert failed")
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	p, err := peerFromLookups(a, "", func(net.Conn, string) (Peer, error) {
		return Peer{Administrator: true}, fmt.Errorf("wrapped: %w", &impersonatedIdentityError{err: cause})
	}, func(net.Conn, string) (Peer, error) {
		t.Error("PID fallback after successful impersonation")
		return Peer{Administrator: true}, nil
	})
	if !errors.Is(err, cause) || p != (Peer{}) {
		t.Fatalf("identity failure did not fail closed: %+v, %v", p, err)
	}
}

func TestPipeDialImpLevelIsIdentification(t *testing.T) {
	t.Parallel()
	if PipeDialImpLevel != 0x00010000 {
		t.Fatalf("PipeDialImpLevel = 0x%x, want SECURITY_IDENTIFICATION 0x10000", PipeDialImpLevel)
	}
	if PipeDialImpLevel == pipeDialImpLevelAnonymous {
		t.Fatal("DialPipe must not use SECURITY_ANONYMOUS")
	}
}

func TestImpersonationPeerAdminVsNonAdmin(t *testing.T) {
	t.Parallel()
	const sid = "S-1-5-21-1-2-3-1001"
	pidMustNotRun := func(net.Conn, string) (Peer, error) {
		t.Error("PID fallback must not run when impersonation succeeds")
		return Peer{Administrator: true}, nil
	}

	adminAuth := authorizerWithLookups("", func(net.Conn, string) (Peer, error) {
		return Peer{SID: sid, Administrator: true}, nil
	}, pidMustNotRun)
	assertAuthorizerPeer(t, adminAuth, func(p Peer) {
		if p.SID != sid || !p.Administrator || !p.CanLinger() {
			t.Fatalf("admin impersonation peer = %+v", p)
		}
	})
	assertServeAllowsLinger(t, adminAuth)

	userAuth := authorizerWithLookups(sid, func(net.Conn, string) (Peer, error) {
		return Peer{SID: sid, Owner: true}, nil
	}, pidMustNotRun)
	assertAuthorizerPeer(t, userAuth, func(p Peer) {
		if p.SID != sid || !p.Owner || p.Administrator || p.CanLinger() {
			t.Fatalf("non-admin impersonation peer = %+v", p)
		}
	})
	assertServeOwnerNoLinger(t, userAuth)
}

func TestPIDFallbackWhenImpersonationFails(t *testing.T) {
	var mu sync.Mutex
	var logs []string
	authLogMu.Lock()
	prev := authLog
	authLog = func(format string, args ...any) {
		mu.Lock()
		logs = append(logs, fmt.Sprintf(format, args...))
		mu.Unlock()
		prev(format, args...)
	}
	authLogMu.Unlock()
	t.Cleanup(func() {
		authLogMu.Lock()
		authLog = prev
		authLogMu.Unlock()
	})

	const sid = "S-1-5-21-1-2-3-1001"
	auth := authorizerWithLookups("", func(net.Conn, string) (Peer, error) {
		return Peer{}, fmt.Errorf("anonymous dial")
	}, func(net.Conn, string) (Peer, error) {
		return Peer{SID: sid, Administrator: true}, nil
	})
	assertAuthorizerPeer(t, auth, func(p Peer) {
		if p.SID != sid || !p.Administrator {
			t.Fatalf("PID fallback peer = %+v", p)
		}
	})
	mu.Lock()
	joined := strings.Join(logs, "\n")
	mu.Unlock()
	if !strings.Contains(joined, "impersonation failed") || !strings.Contains(joined, "anonymous dial") {
		t.Fatalf("PID fallback must be logged, got %q", joined)
	}
}

func TestPeerLookupsFailClosed(t *testing.T) {
	auth := authorizerWithLookups("", func(net.Conn, string) (Peer, error) {
		return Peer{Administrator: true}, fmt.Errorf("impersonation failed")
	}, func(net.Conn, string) (Peer, error) {
		return Peer{Administrator: true}, fmt.Errorf("pid failed")
	})
	p, err := auth(&net.IPConn{})
	if err == nil {
		t.Fatal("both lookups failed: want error")
	}
	if p.Administrator || p.Allowed() {
		t.Fatalf("fail closed must not keep privileges: %+v", p)
	}
	if !strings.Contains(err.Error(), "impersonation") || !strings.Contains(err.Error(), "client process token") {
		t.Fatalf("err = %v", err)
	}
}

func TestServeConnLogsAuthorizerError(t *testing.T) {
	const sentinel = "authorizer-spy-84-token-open-failed"
	var mu sync.Mutex
	var logs []string
	authLogMu.Lock()
	prev := authLog
	authLog = func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		mu.Lock()
		logs = append(logs, msg)
		mu.Unlock()
		prev(format, args...)
	}
	authLogMu.Unlock()
	t.Cleanup(func() {
		authLogMu.Lock()
		authLog = prev
		authLogMu.Unlock()
	})

	called := false
	h := HandlerFunc(func(context.Context, string, json.RawMessage) (any, error) {
		called = true
		return ListUnitsResult{}, nil
	})
	auth := func(net.Conn) (Peer, error) {
		return Peer{Administrator: true}, fmt.Errorf("%s", sentinel)
	}
	client, stop := serveTest(t, h, auth)
	defer stop()
	_, err := client.ListUnits(context.Background())
	pe, ok := err.(*Error)
	if !ok || pe.Code != CodePermissionDenied {
		t.Fatalf("authorizer error must deny, not AllowAdmin: %v", err)
	}
	if called {
		t.Fatal("handler must not run when Authorizer errors")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		joined := strings.Join(logs, "\n")
		mu.Unlock()
		if strings.Contains(joined, sentinel) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	joined := strings.Join(logs, "\n")
	mu.Unlock()
	t.Fatalf("ServeConn must log authorizer error %q, got %q", sentinel, joined)
}

func TestServeConnAuthorizerErrorDoesNotUseReturnedPeer(t *testing.T) {
	t.Parallel()
	called := false
	h := HandlerFunc(func(context.Context, string, json.RawMessage) (any, error) {
		called = true
		return LingerResult{Lingering: true}, nil
	})
	auth := func(net.Conn) (Peer, error) {
		return Peer{Administrator: true, LocalSystem: true}, fmt.Errorf("token open failed")
	}
	client, stop := serveTest(t, h, auth)
	defer stop()
	_, err := client.EnableLinger(context.Background(), "alice")
	pe, ok := err.(*Error)
	if !ok || pe.Code != CodePermissionDenied {
		t.Fatalf("err = %v", err)
	}
	if called {
		t.Fatal("must not proceed as AllowAdmin when Authorizer errors")
	}
}

func assertAuthorizerPeer(t *testing.T, auth Authorizer, check func(Peer)) {
	t.Helper()
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	p, err := auth(c2)
	if err != nil {
		t.Fatal(err)
	}
	check(p)
}

func assertServeAllowsLinger(t *testing.T, auth Authorizer) {
	t.Helper()
	h := HandlerFunc(func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		return LingerResult{Lingering: true, SID: "S-1-5-21-1-2-3-1001"}, nil
	})
	client, stop := serveTest(t, h, auth)
	defer stop()
	got, err := client.EnableLinger(context.Background(), "alice")
	if err != nil {
		t.Fatalf("admin impersonation linger: %v", err)
	}
	if !got.Lingering {
		t.Fatalf("result = %+v", got)
	}
}

func assertServeOwnerNoLinger(t *testing.T, auth Authorizer) {
	t.Helper()
	called := ""
	h := HandlerFunc(func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		called = method
		if method == MethodListUnits {
			return ListUnitsResult{}, nil
		}
		return LingerResult{}, nil
	})
	client, stop := serveTest(t, h, auth)
	defer stop()
	if _, err := client.ListUnits(context.Background()); err != nil {
		t.Fatalf("owner list-units: %v", err)
	}
	_, err := client.EnableLinger(context.Background(), "alice")
	pe, ok := err.(*Error)
	if !ok || pe.Code != CodePermissionDenied {
		t.Fatalf("non-admin impersonation linger err = %v", err)
	}
	if called != MethodListUnits {
		t.Fatal("handler must not run for non-admin linger")
	}
}

func TestDefaultAuthorizerUsesLookupsNotAllowAdmin(t *testing.T) {
	t.Parallel()
	p, err := DefaultAuthorizer()(nil)
	if p.Administrator || p.Allowed() {
		t.Fatalf("DefaultAuthorizer(nil) stamped privileges: %+v (err=%v)", p, err)
	}
	if err == nil {
		t.Fatal("DefaultAuthorizer(nil) must fail closed with an error")
	}
}
