package unit

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestFormatVersionSelectsExplicitPolicies(t *testing.T) {
	for _, tc := range []struct {
		name, text    string
		version       int
		any           bool
		weight, quota uint32
	}{
		{"legacy.path", "[Path]\nPathExists=C:\\Data\\one\nPathExists=C:\\Data\\two\n", 1, false, 0, 0},
		{"any.path", "[Path]\nPathExists=C:\\Data\\one\nPathExists=C:\\Data\\two\n[Unit]\nFormatVersion=2\n", 2, true, 0, 0},
		{"all.path", "[Unit]\nFormatVersion=2\n[Path]\nPathExistsAll=C:\\Data\\one\nPathExistsAll=C:\\Data\\two\n", 2, false, 0, 0},
		{"weight.service", "[Unit]\nFormatVersion=2\n[Service]\nExecStart=C:\\Apps\\worker.exe\nWindowsCPUWeight=9\n", 2, false, 9, 0},
		{"quota.service", "[Unit]\nFormatVersion=2\n[Service]\nExecStart=C:\\Apps\\worker.exe\nWindowsCPUQuota=25%\n", 2, false, 0, 25},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := ParseUnit(tc.name, tc.text)
			if r.HasError() {
				t.Fatal(r.Issues)
			}
			if r.Unit.FormatVersion != tc.version {
				t.Fatal("wrong format", r.Unit.FormatVersion)
			}
			if p := r.Unit.PathWatch; p != nil && p.ExistsAny != tc.any {
				t.Fatal("wrong predicate policy")
			}
			if s := r.Unit.Service; s != nil && (s.WindowsCPUWeight != tc.weight || s.WindowsCPUQuota != tc.quota) {
				t.Fatalf("wrong CPU policy: %+v", s)
			}
		})
	}
}

func TestFormatVersionRejectsAmbiguousOrUnsupportedPolicies(t *testing.T) {
	for _, text := range []string{
		"[Unit]\nFormatVersion=3\n",
		"[Unit]\nFormatVersion=2\nFormatVersion=1\n",
		"[Unit]\nFormatVersion=2\n[Service]\nCPUWeight=50\n",
		"[Unit]\nFormatVersion=2\n[Service]\nCPUQuota=25%\n",
		"[Service]\nWindowsCPUWeight=5\n",
		"[Service]\nWindowsCPUQuota=25%\n",
		"[Unit]\nFormatVersion=2\n[Service]\nWindowsCPUWeight=0\n",
		"[Unit]\nFormatVersion=2\n[Service]\nWindowsCPUWeight=10\n",
		"[Unit]\nFormatVersion=2\n[Service]\nWindowsCPUQuota=101%\n",
		"[Unit]\nFormatVersion=2\n[Service]\nWindowsCPUQuota=25\n",
		"[Unit]\nFormatVersion=2\n[Service]\nWindowsCPUQuota=\n",
		"[Unit]\nFormatVersion=2\n[Service]\nWindowsCPUWeight=5\nWindowsCPUQuota=25%\n",
		"[Unit]\nFormatVersion=2\n[Service]\nType=scm\nServiceName=example\nWindowsCPUWeight=5\n",
	} {
		r := ParseUnit("policy.service", text+"[Service]\nExecStart=C:\\Apps\\worker.exe\n")
		if !r.HasError() {
			t.Fatalf("accepted invalid format policy: %s", text)
		}
	}
	for _, text := range []string{"[Path]\nPathExistsAll=C:\\Data\\ready\n", "[Unit]\nFormatVersion=2\n[Path]\nPathExistsAll=C:\\Data\\one\nPathExists=C:\\Data\\two\n"} {
		if r := ParseUnit("policy.path", text); !r.HasError() {
			t.Fatal("accepted ambiguous path policy", text)
		}
	}
}

func TestFormat2LiteralExecutableDoesNotConsultFilesystem(t *testing.T) {
	r := parseReport("worker.service", "worker.service", []byte("[Unit]\nFormatVersion=2\n[Service]\nExecStart=C:\\Missing Apps\\worker.exe\nExecStartArg=--serve\nExecStop=C:\\Missing Apps\\stop.exe\nExecStopArg=--shutdown\nTimeoutStopSec=10s\n"), func(string) (os.FileInfo, error) {
		t.Fatal("format 2 consulted executable existence")
		return nil, os.ErrNotExist
	})
	if r.HasError() {
		t.Fatal(r.Issues)
	}
	if r.Unit.Service.ExecStart[0] != `C:\Missing Apps\worker.exe` {
		t.Fatal("literal executable was split")
	}
}

func TestMigrationPreservesEffectiveCPUAndPathPolicy(t *testing.T) {
	for _, tc := range []struct{ name, source, want string }{
		{"weight.service", "# keep comment\n[Service]\nExecStart=C:\\Apps\\worker.exe\nCPUWeight=bad-overridden-value\nCPUWeight=5000\n", "WindowsCPUWeight=5"},
		{"quota.service", "[Unit]\nFormatVersion=1\n[Service]\nExecStart=C:\\Apps\\worker.exe\nCPUQuota= \\\n25%\n", "WindowsCPUQuota=25%"},
		{"ready.path", "# keep comment\n[Path]\nPathExists=C:\\Data\\one\nPathExists=C:\\Data\\two\n", "PathExistsAll=C:\\Data\\two"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := []byte(tc.source)
			before := bytes.Clone(src)
			m := ConvertToV2(tc.name, tc.name, src)
			if m.Output == nil {
				t.Fatal(m.Issues)
			}
			if !bytes.Equal(src, before) {
				t.Fatal("source mutated")
			}
			if !strings.Contains(string(m.Output), tc.want) || strings.Count(string(m.Output), "FormatVersion=2") != 1 {
				t.Fatalf("bad preview: %s", m.Output)
			}
			if strings.Contains(tc.source, "# keep comment") && !bytes.Contains(m.Output, []byte("# keep comment")) {
				t.Fatal("comment lost")
			}
			old := ParseUnit(tc.name, tc.source).Unit
			converted := Parse(tc.name, tc.name, m.Output)
			if converted.HasError() {
				t.Fatal(converted.Issues)
			}
			if old.Service != nil {
				if old.Service.CPUWeightSet && converted.Unit.Service.WindowsCPUWeight != WindowsCPUWeight(old.Service.CPUWeight) {
					t.Fatal("effective CPU weight changed")
				}
				if old.Service.CPUQuotaSet && converted.Unit.Service.WindowsCPUQuota != old.Service.CPUQuota {
					t.Fatal("effective quota changed")
				}
			}
			if old.PathWatch != nil && (converted.Unit.PathWatch.ExistsAny || len(converted.Unit.PathWatch.Exists) != len(old.PathWatch.Exists)) {
				t.Fatal("AND path predicate changed")
			}
			again := ConvertToV2(tc.name, tc.name, m.Output)
			if !bytes.Equal(again.Output, m.Output) || len(again.Changes) != 0 {
				t.Fatal("conversion not idempotent")
			}
		})
	}
}

func TestMigrationRejectsInvalidAndOversizeOutput(t *testing.T) {
	for _, src := range []string{"[Unit]\nFormatVersion=9\n", "[Service]\nExecStart=C:\\Apps\\worker.exe\nCPUWeight=oops\n", "#" + strings.Repeat("x", MaxFileBytes-1)} {
		if m := ConvertToV2("worker.service", "worker.service", []byte(src)); m.Output != nil {
			t.Fatal("invalid migration produced output")
		}
	}
	if m := ConvertToV2("large.target", "large.target", []byte("#"+strings.Repeat("x", MaxFileBytes-1))); m.Output != nil {
		t.Fatal("oversized converted output was accepted")
	}
}
