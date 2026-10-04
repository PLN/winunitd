package servicing

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/PLN/winunitd/internal/capability"
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/servicing/servicingtest"
	"github.com/PLN/winunitd/internal/version"
)

func TestReleasePrecedence(t *testing.T) {
	// Semantic-versioning 2.0 example order plus the product's own releases.
	ordered := []string{
		"0.1.0-alpha", "0.1.0", "0.2.0-beta", "0.2.1-beta", "0.2.1",
		"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta",
		"1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.10.0", "10.0.0",
	}
	for i := range ordered {
		for j := range ordered {
			a, err := ParseRelease(ordered[i])
			if err != nil {
				t.Fatal(err)
			}
			b, err := ParseRelease(ordered[j])
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := a.Compare(b); got != want {
				t.Errorf("compare %s %s = %d, want %d", ordered[i], ordered[j], got, want)
			}
		}
	}
	a, _ := ParseRelease("0.2.0+build.7")
	b, _ := ParseRelease("0.2.0")
	if a.Compare(b) != 0 {
		t.Error("build metadata changed the order")
	}
}

func TestParseReleaseRejectsMalformed(t *testing.T) {
	for _, s := range []string{
		"", "1", "1.0", "1.0.0.0", "01.0.0", "1.00.0", "v1.0.0", "1.0.0-", "1.0.0-alpha..1",
		"1.0.0-01", "1.0.0-al pha", "1.0.0+", "1.0.0+a..b", "-1.0.0", "1.0.x", "1.0.0-ü",
	} {
		if _, err := ParseRelease(s); err == nil {
			t.Errorf("%q accepted", s)
		}
	}
}

func TestDecodeFloor(t *testing.T) {
	f, err := DecodeFloor([]byte(`{"schema":1,"minVersion":"0.2.0","requireFeatures":["restart-backoff","exec-stop"],"requireCleanBuild":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if f.MinVersion != "0.2.0" || len(f.RequireFeatures) != 2 || !f.RequireCleanBuild {
		t.Fatalf("floor = %+v", f)
	}
	data, err := EncodeFloor(f)
	if err != nil {
		t.Fatal(err)
	}
	again, err := DecodeFloor(data)
	if err != nil || !slices.Equal(again.RequireFeatures, []string{"exec-stop", "restart-backoff"}) {
		t.Fatalf("encoded floor %s: %+v %v", data, again, err)
	}
	bad := map[string]string{
		"empty":            `{}`,
		"schema":           `{"schema":2,"minVersion":"0.2.0"}`,
		"no requirement":   `{"schema":1}`,
		"version":          `{"schema":1,"minVersion":"latest"}`,
		"unknown field":    `{"schema":1,"minVersion":"0.2.0","maxVersion":"9.0.0"}`,
		"feature name":     `{"schema":1,"requireFeatures":["Exec Stop"]}`,
		"repeated feature": `{"schema":1,"requireFeatures":["exec-stop","exec-stop"]}`,
		"trailing data":    `{"schema":1,"minVersion":"0.2.0"} {}`,
		"trailing ]":       `{"schema":1,"minVersion":"0.1.0"}]`,
		"trailing }":       `{"schema":1,"minVersion":"0.1.0"}}`,
		"trailing text":    `{"schema":1,"minVersion":"0.1.0"} x`,
		"unterminated":     `{"schema":1,"minVersion":"0.1.0"`,
		"repeated version": `{"schema":1,"minVersion":"9.0.0","minVersion":"0.1.0"}`,
		"repeated clean":   `{"schema":1,"minVersion":"0.1.0","requireCleanBuild":true,"requireCleanBuild":false}`,
		"repeated schema":  `{"schema":1,"schema":1,"minVersion":"0.1.0"}`,
		"null clean":       `{"schema":1,"minVersion":"0.1.0","requireCleanBuild":null}`,
		"null version":     `{"schema":1,"minVersion":null,"requireCleanBuild":true}`,
		"null features":    `{"schema":1,"minVersion":"0.1.0","requireFeatures":null}`,
		"null feature":     `{"schema":1,"requireFeatures":["exec-stop",null]}`,
		"null schema":      `{"schema":null,"minVersion":"0.1.0"}`,
		"no schema":        `{"minVersion":"0.1.0"}`,
		"key case":         `{"schema":1,"MinVersion":"0.1.0"}`,
		"lower-case key":   `{"schema":1,"minversion":"0.1.0"}`,
		"clean as string":  `{"schema":1,"requireCleanBuild":"true"}`,
		"version number":   `{"schema":1,"minVersion":2}`,
		"schema string":    `{"schema":"1","minVersion":"0.1.0"}`,
		"schema fraction":  `{"schema":1.0,"minVersion":"0.1.0"}`,
		"features object":  `{"schema":1,"requireFeatures":{"exec-stop":true}}`,
		"byte order mark":  "\ufeff" + `{"schema":1,"minVersion":"0.1.0"}`,
		"not an object":    `["0.2.0"]`,
		"oversize":         `{"schema":1,"minVersion":"0.2.0"}` + strings.Repeat(" ", MaxFloorBytes),
	}
	many := `{"schema":1,"requireFeatures":[`
	for i := 0; i <= maxFloorFeatures; i++ {
		if i > 0 {
			many += ","
		}
		many += fmt.Sprintf(`"f%d"`, i)
	}
	bad["too many features"] = many + `]}`
	for name, data := range bad {
		if _, err := DecodeFloor([]byte(data)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func boolPtr(v bool) *bool { return &v }

func TestEvaluate(t *testing.T) {
	clean := Build{Version: "0.2.0", Commit: "abc", Modified: boolPtr(false), Features: []string{"exec-stop", "job-limits"}}
	cases := []struct {
		name  string
		floor *Floor
		build Build
		want  []string
	}{
		{"no floor", nil, Build{}, nil},
		{"satisfied", &Floor{Schema: 1, MinVersion: "0.2.0", RequireFeatures: []string{"exec-stop"}, RequireCleanBuild: true}, clean, nil},
		{"newer prerelease", &Floor{Schema: 1, MinVersion: "0.2.0-beta"}, Build{Version: "0.2.0-rc.1"}, nil},
		{"older", &Floor{Schema: 1, MinVersion: "0.2.0"}, Build{Version: "0.1.0-alpha"}, []string{"version 0.1.0-alpha is below 0.2.0"}},
		{"prerelease of floor", &Floor{Schema: 1, MinVersion: "0.2.0"}, Build{Version: "0.2.0-beta"}, []string{"version 0.2.0-beta is below 0.2.0"}},
		{"unparsable build version", &Floor{Schema: 1, MinVersion: "0.2.0"}, Build{Version: "dev"}, []string{`build version "dev" is not a release version`}},
		{"missing features", &Floor{Schema: 1, RequireFeatures: []string{"restart-backoff", "exec-stop", "linger-s4u"}}, clean, []string{"missing features linger-s4u, restart-backoff"}},
		{"no feature list", &Floor{Schema: 1, RequireFeatures: []string{"exec-stop"}}, Build{Version: "0.2.0"}, []string{"build reports no features; requires exec-stop"}},
		{"empty feature list", &Floor{Schema: 1, RequireFeatures: []string{"exec-stop"}}, Build{Version: "0.2.0", Features: []string{}}, []string{"missing features exec-stop"}},
		{"no revision", &Floor{Schema: 1, RequireCleanBuild: true}, Build{Version: "0.2.0"}, []string{"build has no source revision"}},
		{"unknown state", &Floor{Schema: 1, RequireCleanBuild: true}, Build{Version: "0.2.0", Commit: "abc"}, []string{"build source state is unknown"}},
		{"modified", &Floor{Schema: 1, RequireCleanBuild: true}, Build{Version: "0.2.0", Commit: "abc", Modified: boolPtr(true)}, []string{"build is from a modified source tree"}},
		{"all three", &Floor{Schema: 1, MinVersion: "1.0.0", RequireFeatures: []string{"exec-stop"}, RequireCleanBuild: true},
			Build{Version: "0.2.0", Features: []string{}}, []string{"version 0.2.0 is below 1.0.0", "missing features exec-stop", "build has no source revision"}},
	}
	for _, c := range cases {
		v := Evaluate(c.floor, c.build)
		if v.Satisfied != (len(c.want) == 0) || !slices.Equal(v.Reasons, c.want) {
			t.Errorf("%s: %+v, want %q", c.name, v, c.want)
		}
	}
}

func TestBuildFrom(t *testing.T) {
	clean := false
	features := []string{"exec-stop"}
	b := BuildFrom(version.BuildInfo{Version: "0.2.0", Commit: "abc", Modified: &clean, Go: "go1.25"}, features)
	if b.Version != "0.2.0" || b.Commit != "abc" || b.Modified == nil || *b.Modified || !slices.Equal(b.Features, features) {
		t.Fatalf("clean build %+v", b)
	}
	// The build owns its copies.
	clean, features[0] = true, "changed"
	if *b.Modified || b.Features[0] != "exec-stop" {
		t.Fatalf("build shares its inputs: %+v", b)
	}
	if b := BuildFrom(version.BuildInfo{Version: "0.2.0"}, nil); b.Commit != "" || b.Modified != nil || b.Features != nil {
		t.Fatalf("unknown identity %+v", b)
	}
}

func floorDir(t *testing.T) (string, string) {
	t.Helper()
	base := servicingtest.DataRoot(t)
	return base, FloorPath(base)
}

// TestFloorStore covers the store's semantics on every system; the
// protection of the record and its directories is tested per system.
func TestFloorStore(t *testing.T) {
	_, path := floorDir(t)
	if f, err := ReadFloor(path); err != nil || f != nil {
		t.Fatalf("absent record: %+v %v", f, err)
	}
	want := &Floor{Schema: 1, MinVersion: "0.2.0", RequireFeatures: []string{"restart-backoff", "exec-stop"}}
	if err := WriteFloor(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFloor(path)
	if err != nil || got.MinVersion != "0.2.0" || !slices.Equal(got.RequireFeatures, []string{"exec-stop", "restart-backoff"}) {
		t.Fatalf("round trip %+v %v", got, err)
	}
	if err := WriteFloor(path, &Floor{Schema: 1, MinVersion: "0.3.0"}); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadFloor(path); err != nil || got.MinVersion != "0.3.0" || got.RequireFeatures != nil {
		t.Fatalf("replacement %+v %v", got, err)
	}
	if err := WriteFloor(path, &Floor{Schema: 1}); err == nil {
		t.Fatal("empty floor written")
	}
	// The record and the floor's lock, which is never removed.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 2 || entries[0].Name() != FloorFileName || entries[1].Name() != LockFileName {
		t.Fatalf("temporary files left: %v %v", entries, err)
	}
	if err := RemoveFloor(path); err != nil {
		t.Fatal(err)
	}
	if err := RemoveFloor(path); err != nil {
		t.Fatalf("second remove: %v", err)
	}
	if f, err := ReadFloor(path); err != nil || f != nil {
		t.Fatalf("removed record: %+v %v", f, err)
	}
}

// A missing data root or daemon directory under a safe container is a
// first install: no floor, and nothing can be written there.
func TestFloorStoreSafeAbsence(t *testing.T) {
	for name, base := range map[string]string{
		"no data root":        filepath.Join(servicingtest.Root(t), "winunitd"),
		"no daemon directory": servicingtest.Root(t),
	} {
		path := FloorPath(base)
		if f, err := ReadFloor(path); err != nil || f != nil {
			t.Fatalf("%s: %+v %v", name, f, err)
		}
		if hold := AdmissionHold(path, Build{Version: "0.1.0"}); hold != "" {
			t.Fatalf("%s held admission: %s", name, hold)
		}
		if err := WriteFloor(path, &Floor{Schema: 1, MinVersion: "0.1.0"}); err == nil {
			t.Fatalf("%s: wrote a record", name)
		}
		if err := RemoveFloor(path); err != nil {
			t.Fatalf("%s: remove: %v", name, err)
		}
	}
}

// Running is the identity the system endpoint's capability query reports, so
// a floor is satisfied by exactly the features that endpoint lists.
func TestRunningBuildReportsSystemFeatures(t *testing.T) {
	b := Running()
	info := version.Build()
	if b.Version != info.Version || b.Commit != info.Commit || !slices.Equal(b.Features, capability.SystemFeatures()) ||
		(b.Modified == nil) != (info.Modified == nil) || (b.Modified != nil && *b.Modified != *info.Modified) {
		t.Fatalf("running build %+v, identity %+v", b, info)
	}
	if !slices.Contains(b.Features, protocol.FeatureRestartBackoff) {
		t.Fatalf("running build features %v", b.Features)
	}
	if v := Evaluate(&Floor{Schema: 1, RequireFeatures: b.Features}, b); !v.Satisfied {
		t.Fatalf("floor of every system feature %+v", v)
	}
	if v := Evaluate(&Floor{Schema: 1, RequireFeatures: []string{"no-such-contract", protocol.FeatureRestartBackoff}}, b); v.Satisfied ||
		!slices.Equal(v.Reasons, []string{"missing features no-such-contract"}) {
		t.Fatalf("floor with an unknown feature %+v", v)
	}
	// A native contract this platform does not enforce holds it like any
	// other missing feature.
	for _, name := range []string{protocol.FeatureExecStop, protocol.FeatureJobLimits, protocol.FeatureLingerS4U} {
		if slices.Contains(b.Features, name) {
			continue
		}
		if v := Evaluate(&Floor{Schema: 1, RequireFeatures: []string{name}}, b); v.Satisfied || !slices.Equal(v.Reasons, []string{"missing features " + name}) {
			t.Fatalf("floor requiring %s: %+v", name, v)
		}
	}
}

func TestAdmissionHold(t *testing.T) {
	_, path := floorDir(t)
	build := Build{Version: "0.1.0-alpha", Commit: "abc", Modified: boolPtr(false)}
	if hold := AdmissionHold(path, build); hold != "" {
		t.Fatalf("no floor held admission: %s", hold)
	}
	if err := WriteFloor(path, &Floor{Schema: 1, MinVersion: "0.1.0-alpha", RequireCleanBuild: true}); err != nil {
		t.Fatal(err)
	}
	if hold := AdmissionHold(path, build); hold != "" {
		t.Fatalf("satisfied floor held admission: %s", hold)
	}
	if err := WriteFloor(path, &Floor{Schema: 1, MinVersion: "0.2.0", RequireFeatures: []string{"exec-stop"}}); err != nil {
		t.Fatal(err)
	}
	hold := AdmissionHold(path, build)
	if hold != "below compatibility floor: version 0.1.0-alpha is below 0.2.0; build reports no features; requires exec-stop" {
		t.Fatalf("hold = %q", hold)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if hold := AdmissionHold(path, build); !strings.HasPrefix(hold, "compatibility floor record is unusable: ") {
		t.Fatalf("malformed record: %q", hold)
	}
}

func TestFloorWithin(t *testing.T) {
	prev := &Floor{Schema: 1, MinVersion: "0.2.0", RequireFeatures: []string{"exec-stop", "job-limits"}, RequireCleanBuild: true}
	within := []*Floor{
		{Schema: 1, MinVersion: "0.2.0"},
		{Schema: 1, MinVersion: "0.1.0", RequireFeatures: []string{"exec-stop"}},
		{Schema: 1, MinVersion: "0.2.0-beta", RequireCleanBuild: true},
		{Schema: 1, RequireFeatures: []string{"job-limits", "exec-stop"}, RequireCleanBuild: true},
		prev,
	}
	for _, f := range within {
		if !f.Within(prev) {
			t.Errorf("%+v is not within %+v", f, prev)
		}
	}
	raises := []*Floor{
		{Schema: 1, MinVersion: "0.2.1"},
		{Schema: 1, MinVersion: "0.2.0", RequireFeatures: []string{"linger-s4u"}},
		{Schema: 1, MinVersion: "1.0.0-alpha"},
	}
	for _, f := range raises {
		if f.Within(prev) {
			t.Errorf("%+v counted as a lowering of %+v", f, prev)
		}
	}
	if (&Floor{Schema: 1, RequireCleanBuild: true}).Within(&Floor{Schema: 1, MinVersion: "0.1.0"}) {
		t.Error("adding the clean-build requirement counted as a lowering")
	}
	if (&Floor{Schema: 1, MinVersion: "0.1.0"}).Within(&Floor{Schema: 1, RequireCleanBuild: true}) {
		t.Error("adding a version requirement counted as a lowering")
	}
	if (&Floor{Schema: 1, MinVersion: "0.1.0"}).Within(nil) {
		t.Error("a first floor counted as a lowering")
	}
}

// Trust is relative to the checking account: an account's own check trusts
// it, but a SYSTEM check, the manager's, does not. Machine principals are
// trusted by every check.
func TestTrustedPrincipalIsCallerRelative(t *testing.T) {
	const (
		a     = "S-1-5-21-1000-2000-3000-1001"
		b     = "S-1-5-21-1000-2000-3000-1002"
		users = "S-1-5-32-545"
		world = "S-1-1-0"
	)
	for _, machine := range []string{sidLocalSystem, sidAdministrators, sidTrustedInstaller} {
		for _, self := range []string{sidLocalSystem, a, ""} {
			if !trustedPrincipal(machine, self) {
				t.Errorf("%s untrusted for the checker %q", machine, self)
			}
		}
	}
	if !trustedPrincipal(a, a) {
		t.Error("an account's own check does not trust it")
	}
	for _, c := range []struct{ sid, self string }{
		{a, sidLocalSystem}, {a, b}, {a, ""}, {users, a}, {users, sidLocalSystem}, {world, sidLocalSystem}, {"", ""},
	} {
		if trustedPrincipal(c.sid, c.self) {
			t.Errorf("%q trusted for the checker %q", c.sid, c.self)
		}
	}
}
