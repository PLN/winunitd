package main

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestMatchRevision(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	with := func(settings ...debug.BuildSetting) *debug.BuildInfo { return &debug.BuildInfo{Settings: settings} }
	if err := matchRevision(with(debug.BuildSetting{Key: "vcs.revision", Value: commit}), commit); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		bi   *debug.BuildInfo
		want string
	}{
		{with(debug.BuildSetting{Key: "vcs.revision", Value: "fedcba9876543210fedcba9876543210fedcba98"}), "does not match"},
		{with(debug.BuildSetting{Key: "vcs.modified", Value: "false"}), "no embedded VCS revision"},
		{with(), "no embedded VCS revision"},
	} {
		if err := matchRevision(tc.bi, commit); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("matchRevision error = %v, want %q", err, tc.want)
		}
	}
}
