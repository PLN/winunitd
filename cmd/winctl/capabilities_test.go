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

// rawCapabilitiesDial answers one request per connection with either an RPC
// error or a raw result member (empty omits it).
func rawCapabilitiesDial(rpcErr *protocol.Error, raw string) func(context.Context) (net.Conn, error) {
	return rawEnvelopeDial(protocol.Name, protocol.Version, rpcErr, raw)
}

// rawEnvelopeDial is rawCapabilitiesDial with a chosen envelope identity.
func rawEnvelopeDial(name string, version int, rpcErr *protocol.Error, raw string) func(context.Context) (net.Conn, error) {
	return func(context.Context) (net.Conn, error) {
		server, client := net.Pipe()
		go func() {
			defer server.Close()
			var req protocol.Request
			if err := json.NewDecoder(server).Decode(&req); err != nil {
				return
			}
			resp := protocol.Response{Protocol: name, Version: version, ID: req.ID, Error: rpcErr}
			if rpcErr == nil && raw != "" {
				resp.Result = json.RawMessage(raw)
			}
			data, _ := json.Marshal(resp)
			_, _ = server.Write(append(data, '\n'))
		}()
		return client, nil
	}
}

// A manager from before the query (such as 0.2.1-beta) answers
// method-not-found from its dispatcher. winctl must treat that as below floor.
func TestCapabilitiesOlderManagerIsBelowFloor(t *testing.T) {
	dial := rawCapabilitiesDial(protocol.ErrMethodNotFound(protocol.MethodCapabilities), "")
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

// Malformed or foreign replies are failures, never an empty capability set.
func TestCapabilitiesRejectsInvalidReplies(t *testing.T) {
	for name, raw := range map[string]string{
		"absent":         "",
		"null":           "null",
		"features only":  `{"features":["restart-backoff"]}`,
		"wrong product":  `{"product":"other","version":"1","go":"go1","platform":"windows/amd64","scope":"system","protocol":{"name":"winunitd.control","version":1,"methods":["capabilities"]},"formatVersions":[1],"features":["restart-backoff"],"jobLimits":[],"userManagerModes":[],"experimentalUserManagerModes":[],"directives":{}}`,
		"wrong protocol": `{"product":"winunitd","version":"1","go":"go1","platform":"windows/amd64","scope":"system","protocol":{"name":"other.control","version":1,"methods":["capabilities"]},"formatVersions":[1],"features":["restart-backoff"],"jobLimits":[],"userManagerModes":[],"experimentalUserManagerModes":[],"directives":{}}`,
	} {
		var out, errOut bytes.Buffer
		code := runCLI([]string{"capabilities", "--require", "restart-backoff"}, &out, &errOut, rawCapabilitiesDial(nil, raw))
		if code != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), "invalid capability reply") {
			t.Errorf("%s: exit %d stdout=%q stderr=%q", name, code, out.String(), errOut.String())
		}
	}
}

// A valid capability body inside a foreign, incompatible or unversioned
// envelope is not a capability reply.
func TestCapabilitiesRejectsForeignEnvelope(t *testing.T) {
	_, dial, stop := startTestDaemon(t)
	var valid bytes.Buffer
	if code := runCLI([]string{"capabilities"}, &valid, &bytes.Buffer{}, dial); code != 0 {
		t.Fatal("reference reply failed")
	}
	stop()
	for _, env := range []struct {
		name    string
		version int
	}{{"other.control", protocol.Version}, {protocol.Name, protocol.Version + 1}, {"", 0}} {
		var out, errOut bytes.Buffer
		code := runCLI([]string{"capabilities", "--require", protocol.FeatureRestartBackoff}, &out, &errOut, rawEnvelopeDial(env.name, env.version, nil, valid.String()))
		if code != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), "response uses protocol") {
			t.Errorf("envelope %q/%d: exit %d stdout=%q stderr=%q", env.name, env.version, code, out.String(), errOut.String())
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
