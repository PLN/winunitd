package protocol

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
)

func validCapabilities() CapabilitiesResult {
	modified := false
	return CapabilitiesResult{
		Product: "winunitd", Version: "1.2.3-test",
		Commit: "0123456789abcdef0123456789abcdef01234567", Modified: &modified,
		Go: "go1.0-test", Platform: "windows/amd64", Scope: "system",
		Protocol:       ProtocolCapabilities{Name: Name, Version: Version, Methods: []string{MethodStatus, MethodCapabilities}},
		FormatVersions: []int{1, 2}, Features: []string{FeatureRestartBackoff},
		JobLimits: []string{}, UserManagerModes: []string{}, ExperimentalUserManagerModes: []string{},
		Directives: map[string][]string{"Service": {"ExecStart"}},
	}
}

func TestCapabilitiesValidate(t *testing.T) {
	t.Parallel()
	ok := validCapabilities()
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	noVCS := validCapabilities()
	noVCS.Commit, noVCS.Modified = "", nil
	if err := noVCS.Validate(); err != nil {
		t.Fatalf("binary without VCS data rejected: %v", err)
	}
	for name, mutate := range map[string]func(*CapabilitiesResult){
		"product":            func(r *CapabilitiesResult) { r.Product = "other" },
		"version":            func(r *CapabilitiesResult) { r.Version = "" },
		"platform":           func(r *CapabilitiesResult) { r.Platform = "" },
		"scope":              func(r *CapabilitiesResult) { r.Scope = "" },
		"protocol name":      func(r *CapabilitiesResult) { r.Protocol.Name = "other.control" },
		"protocol version":   func(r *CapabilitiesResult) { r.Protocol.Version = Version + 1 },
		"own method":         func(r *CapabilitiesResult) { r.Protocol.Methods = []string{MethodStatus} },
		"format versions":    func(r *CapabilitiesResult) { r.FormatVersions = nil },
		"directives":         func(r *CapabilitiesResult) { r.Directives = nil },
		"features":           func(r *CapabilitiesResult) { r.Features = nil },
		"job limits":         func(r *CapabilitiesResult) { r.JobLimits = nil },
		"modes":              func(r *CapabilitiesResult) { r.UserManagerModes = nil },
		"experimental modes": func(r *CapabilitiesResult) { r.ExperimentalUserManagerModes = nil },
		"modified alone":     func(r *CapabilitiesResult) { r.Commit = "" },
		"short commit":       func(r *CapabilitiesResult) { r.Commit = "0123456" },
		"upper-case commit":  func(r *CapabilitiesResult) { r.Commit = strings.ToUpper(r.Commit) },
	} {
		r := validCapabilities()
		mutate(&r)
		if err := r.Validate(); err == nil {
			t.Errorf("%s: invalid reply accepted", name)
		}
	}
	var none *CapabilitiesResult
	if none.Validate() == nil {
		t.Fatal("nil reply accepted")
	}
}

// serveRawCapabilities answers one request with the given raw result
// member; an empty raw omits it.
func serveRawCapabilities(t *testing.T, raw string) *Client {
	t.Helper()
	server, client := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	go func() {
		defer server.Close()
		var req Request
		if err := json.NewDecoder(server).Decode(&req); err != nil {
			return
		}
		resp := Response{Protocol: Name, Version: Version, ID: req.ID}
		if raw != "" {
			resp.Result = json.RawMessage(raw)
		}
		data, _ := json.Marshal(resp)
		_, _ = server.Write(append(data, '\n'))
	}()
	return NewClient(client)
}

func TestClientCapabilitiesRejectsInvalidReplies(t *testing.T) {
	t.Parallel()
	valid, err := json.Marshal(validCapabilities())
	if err != nil {
		t.Fatal(err)
	}
	withField := func(key, value string) string {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(valid, &m); err != nil {
			t.Fatal(err)
		}
		m[key] = json.RawMessage(value)
		out, _ := json.Marshal(m)
		return string(out)
	}
	for name, raw := range map[string]string{
		"absent":           "",
		"null":             "null",
		"empty object":     "{}",
		"features only":    `{"features":["restart-backoff"]}`,
		"wrong product":    withField("product", `"other"`),
		"wrong protocol":   withField("protocol", `{"name":"other.control","version":1,"methods":["capabilities"]}`),
		"wrong type":       withField("features", `"restart-backoff"`),
		"modified, no vcs": withField("commit", `""`),
	} {
		if got, err := serveRawCapabilities(t, raw).Capabilities(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid capability reply") {
			t.Errorf("%s: reply %+v accepted (%v)", name, got, err)
		}
	}
	got, err := serveRawCapabilities(t, withField("futureField", `{"added":true}`)).Capabilities(context.Background())
	if err != nil || got.Product != "winunitd" {
		t.Fatalf("unknown additional field rejected: %v", err)
	}
}
