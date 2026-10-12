package version

import (
	"runtime"
	"runtime/debug"
	"testing"
)

func TestBuildInfoReadsVCSSettings(t *testing.T) {
	t.Parallel()
	const commit = "0123456789abcdef0123456789abcdef01234567"
	for _, tc := range []struct {
		modified string
		want     *bool
	}{
		{modified: "false", want: new(false)},
		{modified: "true", want: new(true)},
		{modified: "", want: nil},
		{modified: "unexpected", want: nil},
	} {
		bi := &debug.BuildInfo{GoVersion: "go1.0-test", Settings: []debug.BuildSetting{
			{Key: "-trimpath", Value: "true"},
			{Key: "vcs", Value: "git"},
			{Key: "vcs.revision", Value: commit},
			{Key: "vcs.modified", Value: tc.modified},
		}}
		got := buildInfo("1.2.3-test", bi)
		if got.Version != "1.2.3-test" || got.Commit != commit || got.Go != "go1.0-test" {
			t.Fatalf("modified=%q: got %+v", tc.modified, got)
		}
		if (got.Modified == nil) != (tc.want == nil) || (got.Modified != nil && *got.Modified != *tc.want) {
			t.Fatalf("modified=%q: Modified = %v, want %v", tc.modified, got.Modified, tc.want)
		}
	}
}

func TestBuildInfoWithoutVCSIsUnbound(t *testing.T) {
	t.Parallel()
	// A modified flag without a revision does not identify a build.
	bi := &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.modified", Value: "false"}}}
	for _, got := range []BuildInfo{buildInfo("1.2.3", nil), buildInfo("1.2.3", bi)} {
		if got.Commit != "" || got.Modified != nil {
			t.Fatalf("unbound build reported identity: %+v", got)
		}
		if got.Version != "1.2.3" || got.Go != runtime.Version() {
			t.Fatalf("got %+v", got)
		}
	}
}

func TestBuildReportsRelease(t *testing.T) {
	t.Parallel()
	got := Build()
	if got.Version != Version || got.Go == "" {
		t.Fatalf("Build() = %+v", got)
	}
}
