package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"slices"
	"strings"
	"testing"

	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/version"
)

func TestCapabilitiesCommandPrintsJSON(t *testing.T) {
	m, dial, stop := startTestDaemon(t)
	defer stop()
	var out, errOut bytes.Buffer
	if code := runCLI([]string{"capabilities", "--require", protocol.FeatureRestartBackoff}, &out, &errOut, dial); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	var got protocol.CapabilitiesResult
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Product != "winunitd" || got.Version != version.Version || got.Scope != "system" ||
		!slices.Equal(got.FormatVersions, []int{1, 2}) || !slices.Contains(got.Features, protocol.FeatureRestartBackoff) ||
		!slices.Contains(got.Protocol.Methods, protocol.MethodCapabilities) || !slices.Contains(got.Directives["Service"], "ExecStop") {
		t.Fatalf("capabilities = %+v", got)
	}
	st, err := m.Status("foo.service")
	if err != nil || st.Unit.ActiveState != "inactive" {
		t.Fatalf("capabilities changed unit state: %+v %v", st, err)
	}
}

func TestCapabilitiesRequireFailsClosed(t *testing.T) {
	_, dial, stop := startTestDaemon(t)
	defer stop()
	var out, errOut bytes.Buffer
	code := runCLI([]string{"capabilities", "--require=restart-backoff,no-such-feature", "--require", "no-such-feature"}, &out, &errOut, dial)
	if code != 1 {
		t.Fatalf("exit %d, want 1: %s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "missing required feature(s): no-such-feature\n") {
		t.Fatalf("stderr = %q", errOut.String())
	}
	var got protocol.CapabilitiesResult
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.Product != "winunitd" {
		t.Fatalf("JSON not printed on unmet requirement: %q %v", out.String(), err)
	}
}

// A manager from before the query (such as 0.2.1-beta) answers
// method-not-found from its dispatcher. winctl must treat that as below floor.
func TestCapabilitiesOlderManagerIsBelowFloor(t *testing.T) {
	dial := func(context.Context) (net.Conn, error) {
		server, client := net.Pipe()
		go func() {
			defer server.Close()
			var req protocol.Request
			if err := json.NewDecoder(server).Decode(&req); err != nil {
				return
			}
			resp := protocol.Response{Protocol: protocol.Name, Version: protocol.Version, ID: req.ID, Error: protocol.ErrMethodNotFound(req.Method)}
			data, _ := json.Marshal(resp)
			_, _ = server.Write(append(data, '\n'))
		}()
		return client, nil
	}
	for _, args := range [][]string{{"capabilities"}, {"capabilities", "--require", "exec-stop"}} {
		var out, errOut bytes.Buffer
		if code := runCLI(args, &out, &errOut, dial); code != 1 {
			t.Fatalf("%v exit %d, want 1", args, code)
		}
		if out.Len() != 0 || !strings.Contains(errOut.String(), `unknown method "capabilities"`) ||
			!strings.Contains(errOut.String(), "below any capability floor") {
			t.Fatalf("%v stdout=%q stderr=%q", args, out.String(), errOut.String())
		}
	}
}

func TestCapabilitiesUsage(t *testing.T) {
	for _, args := range [][]string{
		{"capabilities", "--require"},
		{"capabilities", "--require="},
		{"capabilities", "--require", "a,,b"},
		{"capabilities", "extra"},
	} {
		var out, errOut bytes.Buffer
		if code := runCLI(args, &out, &errOut, nil); code != 2 {
			t.Fatalf("%v exit %d, want 2", args, code)
		}
	}
	var out, errOut bytes.Buffer
	if code := runCLI([]string{"capabilities", "--help"}, &out, &errOut, nil); code != 0 || !strings.Contains(out.String(), "below any capability floor") {
		t.Fatalf("help exit %d: %q", code, out.String())
	}
	out.Reset()
	if code := runCLI([]string{"--help"}, &out, &errOut, nil); code != 0 || !strings.Contains(out.String(), "capabilities [--require NAME]") {
		t.Fatalf("main help: %q", out.String())
	}
}
