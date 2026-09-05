package main

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testAPI(t *testing.T, handler http.HandlerFunc) *api {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	token := filepath.Join(dir, "token.txt")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(token, []byte("PVEAPIToken=lab@pve!test=fixture-only"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := newAPI(server.URL, ca, token)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestAPIRejectsRedirectAndRedactsResponse(t *testing.T) {
	reached := false
	a := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api2/json/redirect" {
			http.Redirect(w, r, "/untrusted", http.StatusFound)
			return
		}
		if r.URL.Path == "/untrusted" {
			reached = true
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("private-host-and-token"))
	})
	if err := a.call(context.Background(), http.MethodGet, "/redirect", nil, nil); err == nil {
		t.Fatal("redirect accepted")
	}
	if reached {
		t.Fatal("redirect target received a request")
	}
	err := a.call(context.Background(), http.MethodGet, "/denied", nil, nil)
	if err == nil || strings.Contains(err.Error(), "private-host") {
		t.Fatalf("response not redacted: %v", err)
	}
}

func TestAPIRequiresTrustedTLSAndToken(t *testing.T) {
	a := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "PVEAPIToken=lab@pve!test=fixture-only" {
			t.Error("token missing")
		}
		_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
	})
	var result struct {
		OK bool `json:"ok"`
	}
	if err := a.call(context.Background(), http.MethodGet, "/probe", nil, &result); err != nil || !result.OK {
		t.Fatalf("trusted request failed: %v", err)
	}
	untrusted := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("untrusted server received credentials") }))
	defer untrusted.Close()
	a.base = untrusted.URL
	// httptest shares its test certificate, so use system trust instead of the fixture CA.
	a.client.Transport.(*http.Transport).TLSClientConfig.RootCAs = nil
	if err := a.call(context.Background(), http.MethodGet, "/probe", nil, nil); err == nil {
		t.Fatal("untrusted TLS accepted")
	}
}

func TestAPIRejectsUnsafeOrigins(t *testing.T) {
	for _, origin := range []string{"http://lab.example.com", "https://alice:password@lab.example.com", "https://lab.example.com/path", "https://lab.example.com?token=x"} {
		if _, err := newAPI(origin, "", ""); err == nil {
			t.Fatalf("accepted unsafe origin")
		}
	}
}
