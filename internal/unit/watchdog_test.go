package unit

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseTCPWatchdogEndpoint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    string
		wantErr string
	}{
		{in: "127.0.0.1:8080", want: "127.0.0.1:8080"},
		{in: "[::1]:8080", want: "[::1]:8080"},
		{in: "localhost:80", want: "127.0.0.1:80"},
		{in: "8.8.8.8:80", wantErr: "not a loopback address"},
		{in: "example.com:80", wantErr: "not a loopback address"},
		{in: "0.0.0.0:80", wantErr: "not a loopback address"},
		{in: "http://127.0.0.1:8080", wantErr: "host:port, not a URL"},
		{in: "127.0.0.1", wantErr: "invalid WatchdogEndpoint"},
		{in: "127.0.0.1:0", wantErr: "invalid WatchdogEndpoint port"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			got, err := parseTCPWatchdogEndpoint(tt.in)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseHTTPWatchdogEndpoint(t *testing.T) {
	t.Parallel()
	u, err := parseHTTPWatchdogEndpoint("http://127.0.0.1:8080/health")
	if err != nil {
		t.Fatal(err)
	}
	if u.String() != "http://127.0.0.1:8080/health" {
		t.Fatalf("url = %q", u.String())
	}
	u, err = parseHTTPWatchdogEndpoint("https://[::1]/ready")
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "[::1]:443" || u.Path != "/ready" {
		t.Fatalf("https = %s", u)
	}
	_, err = parseHTTPWatchdogEndpoint("http://8.8.8.8/health")
	if err == nil || !strings.Contains(err.Error(), "not a loopback address") {
		t.Fatalf("err = %v", err)
	}
	_, err = parseHTTPWatchdogEndpoint("127.0.0.1:8080")
	if err == nil || !strings.Contains(err.Error(), "http or https") {
		t.Fatalf("err = %v", err)
	}
}

func TestProbeTCPConnectAndRefused(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := ProbeTCP(ctx, addr); err != nil {
		t.Fatalf("listen: %v", err)
	}
	_ = ln.Close()
	if err := ProbeTCP(ctx, addr); err == nil {
		t.Fatal("expected refused after close")
	}
}

func TestProbeTCPWrongHostDoesNotDial(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	err := ProbeTCP(ctx, "8.8.8.8:80")
	if err == nil {
		t.Fatal("expected loopback error")
	}
	if !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Fatalf("wrong-host probed the network: %v", time.Since(start))
	}
}

func TestProbeHTTPStatus(t *testing.T) {
	t.Parallel()
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(ok.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := ProbeHTTP(ctx, ok.URL, 200); err != nil {
		t.Fatal(err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
	}))
	t.Cleanup(bad.Close)
	err := ProbeHTTP(ctx, bad.URL, 200)
	if err == nil || !strings.Contains(err.Error(), "status 503") {
		t.Fatalf("err = %v", err)
	}
}

func TestProbeHTTPWrongHostDoesNotDial(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	err := ProbeHTTP(ctx, "http://example.com/health", 200)
	if err == nil {
		t.Fatal("expected loopback error")
	}
	if !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Fatalf("wrong-host probed the network: %v", time.Since(start))
	}
}

func TestProbeHTTPDoesNotFollowRedirect(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://8.8.8.8/", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := ProbeHTTP(ctx, srv.URL, 200)
	if err == nil || !strings.Contains(err.Error(), "status 302") {
		t.Fatalf("err = %v (must not follow off-host redirect)", err)
	}
}

func TestServiceSpecProbeWatchdog(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	spec := &ServiceSpec{
		WatchdogMode: WatchdogModeTCP,
		WatchdogAddr: ln.Addr().String(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := spec.ProbeWatchdog(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestWatchdogProbeTimeout(t *testing.T) {
	t.Parallel()
	if got := WatchdogProbeTimeout(30 * time.Second); got != 30*time.Second {
		t.Fatalf("interval = %v", got)
	}
	if got := WatchdogProbeTimeout(500 * time.Millisecond); got != 500*time.Millisecond {
		t.Fatalf("subsecond = %v", got)
	}
	if got := WatchdogProbeTimeout(0); got != time.Second {
		t.Fatalf("zero = %v", got)
	}
	if got := WatchdogProbeTimeout(-time.Second); got != time.Second {
		t.Fatalf("negative = %v", got)
	}
}
