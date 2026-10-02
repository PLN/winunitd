package manager

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
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
	if got.Protocol.Name != protocol.Name || got.Protocol.Version != protocol.Version ||
		!slices.Equal(got.Protocol.Methods, protocol.Methods) || !slices.Contains(got.Protocol.Methods, protocol.MethodCapabilities) {
		t.Fatalf("protocol = %+v", got.Protocol)
	}
	if !slices.Equal(got.FormatVersions, unit.FormatVersions()) {
		t.Fatalf("formatVersions = %v", got.FormatVersions)
	}
	want := platformCapabilities()
	features := append([]string{protocol.FeatureRestartBackoff}, want.features...)
	slices.Sort(features)
	if !slices.Equal(got.Features, features) || !slices.IsSorted(got.Features) {
		t.Fatalf("features = %v, want %v", got.Features, features)
	}
	if got.JobLimits == nil || got.UserManagerModes == nil || !slices.Equal(got.JobLimits, nonNil(want.jobLimits)) || !slices.Equal(got.UserManagerModes, nonNil(want.userModes)) {
		t.Fatalf("platform lists = %v %v", got.JobLimits, got.UserManagerModes)
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

func TestCapabilitiesReportUserScopeAndStayStatic(t *testing.T) {
	t.Parallel()
	m, err := New(Config{BaseDir: t.TempDir(), Launch: &fakeLauncher{}, UserScope: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopAll(m) })
	first := m.Capabilities()
	if first.Scope != "user" {
		t.Fatalf("scope = %q", first.Scope)
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
}

// Every advertised job limit must reach runtime.JobLimits for a valid file.
func TestJobLimitDirectivesReachTheJob(t *testing.T) {
	t.Parallel()
	settings := map[string]string{
		"CPUQuota":         "CPUQuota=25%",
		"CPUWeight":        "CPUWeight=100",
		"IoPriority":       "IoPriority=low",
		"MemoryMax":        "MemoryMax=1G",
		"PriorityClass":    "PriorityClass=idle",
		"ProcessLimit":     "ProcessLimit=4",
		"WindowsCPUQuota":  "WindowsCPUQuota=25%",
		"WindowsCPUWeight": "WindowsCPUWeight=5",
	}
	if len(settings) != len(jobLimitDirectives) {
		t.Fatalf("settings cover %d directives, list has %d", len(settings), len(jobLimitDirectives))
	}
	recognized := unit.Directives()["Service"]
	for _, name := range jobLimitDirectives {
		line, ok := settings[name]
		if !ok || !slices.Contains(recognized, name) {
			t.Fatalf("%s: no setting or not recognized", name)
		}
		format := "1"
		if name == "WindowsCPUQuota" || name == "WindowsCPUWeight" {
			format = "2"
		}
		r := unit.ParseUnit("limit.service", "[Unit]\nFormatVersion="+format+"\n[Service]\nExecStart=C:\\Tools\\foo.exe\nWorkingDirectory=C:\\Tools\n"+line+"\n")
		if r.HasError() || r.Unit.Service == nil {
			t.Fatalf("%s: %+v", name, r.Issues)
		}
		if wruntime.JobLimitsFromSpec(r.Unit.Service) == (wruntime.JobLimits{}) {
			t.Fatalf("%s does not reach the Job Object limits", name)
		}
	}
}

func serveCapabilities(t *testing.T, h protocol.Handler) *protocol.Client {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = protocol.Serve(ctx, lis, h, protocol.AllowAdmin)
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
