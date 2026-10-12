package manager

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/PLN/winunitd/internal/journal"
	"github.com/PLN/winunitd/internal/protocol"
	wruntime "github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/unit"
	"github.com/PLN/winunitd/internal/version"
)

func TestCapabilitiesOverControlWithoutStartingUnits(t *testing.T) {
	t.Parallel()
	launch := &fakeLauncher{}
	dir := t.TempDir()
	units := filepath.Join(dir, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		t.Fatal(err)
	}
	writeUnit(t, units, "foo.service", "[Service]\nExecStart=C:\\Tools\\foo.exe\nWorkingDirectory=C:\\Tools\n")
	m, err := New(Config{BaseDir: dir, Launch: launch})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	if _, err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	client := serveCapabilities(t, &Control{Units: m})
	got, err := client.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b := version.Build()
	if got.Product != "winunitd" || got.Version != version.Version || got.Commit != b.Commit || got.Go != b.Go ||
		got.Platform != runtime.GOOS+"/"+runtime.GOARCH || got.Scope != "system" {
		t.Fatalf("identity = %+v", got)
	}
	if got.Protocol.Name != protocol.Name || got.Protocol.Version != protocol.Version || !slices.Equal(got.Protocol.Methods, protocol.Methods) {
		t.Fatalf("system protocol = %+v", got.Protocol)
	}
	if !slices.Equal(got.FormatVersions, unit.FormatVersions()) || !slices.IsSorted(got.Features) || !slices.Contains(got.Features, protocol.FeatureRestartBackoff) {
		t.Fatalf("formats/features = %v %v", got.FormatVersions, got.Features)
	}
	for section, names := range unit.Directives() {
		if !slices.Equal(got.Directives[section], names) {
			t.Fatalf("[%s] = %v, want %v", section, got.Directives[section], names)
		}
	}
	launch.mu.Lock()
	starts := len(launch.starts)
	launch.mu.Unlock()
	st, err := m.Status("foo.service")
	if err != nil {
		t.Fatal(err)
	}
	if starts != 0 || st.Unit.ActiveState != "inactive" {
		t.Fatalf("capabilities started work: starts=%d state=%s", starts, st.Unit.ActiveState)
	}
}

// The reported methods are exactly those the endpoint dispatches. Invalid
// params keep every probe free of side effects.
func TestCapabilitiesListOnlyImplementedMethods(t *testing.T) {
	t.Parallel()
	user, err := New(Config{BaseDir: t.TempDir(), Launch: &fakeLauncher{}, UserScope: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(user) })
	system, err := New(Config{BaseDir: t.TempDir(), Launch: &fakeLauncher{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(system) })
	control := &Control{Units: system}
	for _, tc := range []struct {
		name    string
		handler protocol.Handler
		methods []string
	}{
		{"user", user, user.Capabilities().Protocol.Methods},
		{"system", control, control.Capabilities().Protocol.Methods},
	} {
		for _, method := range protocol.Methods {
			_, err := tc.handler.Handle(context.Background(), method, json.RawMessage(`[]`))
			var pe *protocol.Error
			missing := errors.As(err, &pe) && pe.Code == protocol.CodeMethodNotFound
			if listed := slices.Contains(tc.methods, method); listed == missing {
				t.Errorf("%s endpoint: %s listed=%t, method-not-found=%t", tc.name, method, listed, missing)
			}
		}
	}
}

// A user endpoint answers owners and administrators alike: build metadata as
// on the system endpoint, but only its own methods and features.
func TestUserEndpointCapabilitiesOverRPC(t *testing.T) {
	t.Parallel()
	user, err := New(Config{BaseDir: t.TempDir(), Launch: &fakeLauncher{}, UserScope: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(user) })
	system, err := New(Config{BaseDir: t.TempDir(), Launch: &fakeLauncher{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(system) })
	sys := (&Control{Units: system, Users: &UserHost{}}).Capabilities()
	var replies []*protocol.CapabilitiesResult
	for _, tc := range []struct {
		name   string
		auth   protocol.Authorizer
		linger string
	}{
		{"owner", protocol.AllowOwner, protocol.CodePermissionDenied},
		{"administrator", protocol.AllowAdmin, protocol.CodeMethodNotFound},
	} {
		client := serveCapabilitiesAs(t, user, tc.auth)
		got, err := client.Capabilities(context.Background())
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got.Scope != "user" || got.Product != sys.Product || got.Version != sys.Version || got.Commit != sys.Commit ||
			!reflect.DeepEqual(got.Modified, sys.Modified) || got.Go != sys.Go || got.Platform != sys.Platform ||
			!slices.Equal(got.FormatVersions, sys.FormatVersions) || !reflect.DeepEqual(got.Directives, sys.Directives) {
			t.Fatalf("%s: build metadata differs from the system endpoint: %+v", tc.name, got)
		}
		for _, method := range controlMethods {
			if slices.Contains(got.Protocol.Methods, method) {
				t.Fatalf("%s: user endpoint lists %s", tc.name, method)
			}
		}
		if slices.Contains(got.Features, protocol.FeatureLingerS4U) || len(got.UserManagerModes) != 0 || len(got.ExperimentalUserManagerModes) != 0 {
			t.Fatalf("%s: user endpoint lists system features: %v %v %v", tc.name, got.Features, got.UserManagerModes, got.ExperimentalUserManagerModes)
		}
		var pe *protocol.Error
		if _, err := client.EnableLinger(context.Background(), "alice"); !errors.As(err, &pe) || pe.Code != tc.linger {
			t.Fatalf("%s: enable-linger on the user endpoint = %v, want %s", tc.name, err, tc.linger)
		}
		replies = append(replies, got)
	}
	if !reflect.DeepEqual(replies[0], replies[1]) {
		t.Fatalf("user reply depends on the caller: %+v vs %+v", replies[0], replies[1])
	}
}

func TestCapabilitiesReportUserScopeAndStayStatic(t *testing.T) {
	t.Parallel()
	m, err := New(Config{BaseDir: t.TempDir(), Launch: &fakeLauncher{}, UserScope: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	first := m.Capabilities()
	if first.Scope != "user" || first.Validate() != nil {
		t.Fatalf("user capabilities = %+v (%v)", first, first.Validate())
	}
	first.Features = append(first.Features, "mutated")
	first.Directives["Service"][0] = "Mutated"
	first.Protocol.Methods[0] = "mutated"
	second := m.Capabilities()
	if slices.Contains(second.Features, "mutated") || second.Directives["Service"][0] == "Mutated" || second.Protocol.Methods[0] == "mutated" {
		t.Fatalf("capabilities share mutable state: %+v", second)
	}
	if protocol.Methods[0] == "mutated" {
		t.Fatal("capabilities exposed protocol.Methods")
	}
	control := (&Control{Units: m}).Capabilities()
	control.Protocol.Methods[0] = "mutated"
	if protocol.Methods[0] == "mutated" {
		t.Fatal("control capabilities exposed protocol.Methods")
	}
}

// Each advertised job limit reaches exactly its runtime.JobLimits field.
// IoPriority is a process setting and is not advertised as a job limit.
func TestJobLimitDirectivesReachTheJob(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		line   string
		format string
		want   wruntime.JobLimits
	}{
		"CPUQuota":         {"CPUQuota=25%", "1", wruntime.JobLimits{CPURate: 2500}},
		"CPUWeight":        {"CPUWeight=5000", "1", wruntime.JobLimits{CPUWeight: 5}},
		"MemoryMax":        {"MemoryMax=1G", "1", wruntime.JobLimits{MemoryMax: 1 << 30}},
		"PriorityClass":    {"PriorityClass=idle", "1", wruntime.JobLimits{PriorityClass: wruntime.PriorityIdle}},
		"ProcessLimit":     {"ProcessLimit=4", "1", wruntime.JobLimits{ProcessLimit: 4}},
		"WindowsCPUQuota":  {"WindowsCPUQuota=40%", "2", wruntime.JobLimits{CPURate: 4000}},
		"WindowsCPUWeight": {"WindowsCPUWeight=7", "2", wruntime.JobLimits{CPUWeight: 7}},
	}
	if len(cases) != len(jobLimitDirectives) || slices.Contains(jobLimitDirectives, "IoPriority") {
		t.Fatalf("job limit list = %v", jobLimitDirectives)
	}
	limitsFor := func(format, line string) wruntime.JobLimits {
		t.Helper()
		r := unit.ParseUnit("limit.service", "[Unit]\nFormatVersion="+format+"\n[Service]\nExecStart=C:\\Tools\\foo.exe\nWorkingDirectory=C:\\Tools\n"+line+"\n")
		if r.HasError() || r.Unit.Service == nil {
			t.Fatalf("%s: %+v", line, r.Issues)
		}
		return wruntime.JobLimitsFromSpec(r.Unit.Service)
	}
	recognized := unit.Directives()["Service"]
	for _, name := range jobLimitDirectives {
		tc, ok := cases[name]
		if !ok || !slices.Contains(recognized, name) {
			t.Fatalf("%s: no case or not recognized", name)
		}
		if got := limitsFor(tc.format, tc.line); got != tc.want {
			t.Fatalf("%s: limits %+v, want %+v", name, got, tc.want)
		}
	}
	io := limitsFor("1", "IoPriority=low")
	if !io.IoPrioritySet || io.MemoryMax != 0 || io.ProcessLimit != 0 || io.PriorityClass != 0 || io.CPUWeight != 0 || io.CPURate != 0 {
		t.Fatalf("IoPriority limits = %+v", io)
	}
}

func TestRuntimeReferenceDocumentsFeatures(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "RUNTIME-REFERENCE.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{protocol.FeatureExecStop, protocol.FeatureRestartBackoff, protocol.FeatureJobLimits, protocol.FeatureLingerS4U} {
		if !strings.Contains(string(data), "| `"+name+"` |") {
			t.Fatalf("RUNTIME-REFERENCE.md does not document feature %s", name)
		}
	}
}

func serveCapabilities(t *testing.T, h protocol.Handler) *protocol.Client {
	t.Helper()
	return serveCapabilitiesAs(t, h, protocol.AllowAdmin)
}

func serveCapabilitiesAs(t *testing.T, h protocol.Handler, auth protocol.Authorizer) *protocol.Client {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = protocol.Serve(ctx, lis, h, auth)
	}()
	conn, err := net.Dial(lis.Addr().Network(), lis.Addr().String())
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		cancel()
		<-done
	})
	return protocol.NewClient(conn)
}

func TestDaemonOpenRecordsBuildIdentity(t *testing.T) {
	t.Parallel()
	commit := "0123456789abcdef0123456789abcdef01234567"
	modified := true
	for _, tc := range []struct {
		build version.BuildInfo
		want  string
	}{
		{version.BuildInfo{Version: "1.2.3-test"}, "version 1.2.3-test"},
		{version.BuildInfo{Version: "1.2.3-test", Commit: commit, Modified: new(false)}, "version 1.2.3-test commit " + commit},
		{version.BuildInfo{Version: "1.2.3-test", Commit: commit, Modified: &modified}, "version 1.2.3-test commit " + commit + " modified"},
	} {
		if got := buildReason(tc.build); got != tc.want {
			t.Fatalf("buildReason(%+v) = %q, want %q", tc.build, got, tc.want)
		}
		if cleaned, ok := journal.PublicDetail(tc.want); !ok || cleaned != tc.want {
			t.Fatalf("daemon log would drop %q", tc.want)
		}
	}

	dir := t.TempDir()
	m, err := New(Config{BaseDir: dir, Launch: &fakeLauncher{}})
	if err != nil {
		t.Fatal(err)
	}
	want := buildReason(version.Build())
	st, err := m.Status("")
	if err != nil || len(st.Machine.DaemonEvents) == 0 || st.Machine.DaemonEvents[0].Code != journal.DaemonEventOpen || st.Machine.DaemonEvents[0].Reason != want {
		t.Fatalf("daemon.open = %+v %v", st, err)
	}
	m.Close()
	// A later manager on the same data, as after servicing, reads the record
	// back from the file tail.
	next, err := New(Config{BaseDir: dir, Launch: &fakeLauncher{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(next.Close)
	st, err = next.Status("")
	if err != nil {
		t.Fatal(err)
	}
	opens := 0
	for _, ev := range st.Machine.DaemonEvents {
		if ev.Code == journal.DaemonEventOpen && ev.Reason == want {
			opens++
		}
	}
	if opens != 2 {
		t.Fatalf("daemon.open records with build identity = %d: %+v", opens, st.Machine.DaemonEvents)
	}
}
