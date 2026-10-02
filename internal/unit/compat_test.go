package unit

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

// beta021Directives is the directive table of the published 0.2.1-beta
// (tag v0.2.1-beta). That parser reports any other name as an error
// ("unknown directive"), not a warning.
var beta021Directives = map[string][]string{
	"Unit": {"After", "Before", "BindsTo", "Description", "PartOf", "Requires",
		"RequiresInteractiveSession", "StartLimitBurst", "StartLimitIntervalSec", "Wants"},
	"Service": {"CPUQuota", "CPUWeight", "Environment", "ExecStart", "ExecStartArg",
		"IoPriority", "MemoryMax", "NotifyAccess", "PriorityClass", "ProcessLimit",
		"Restart", "RestartSec", "ServiceName", "TaskName", "TimeoutStartSec",
		"TimeoutStopSec", "Type", "WatchdogEndpoint", "WatchdogExpectedStatus",
		"WatchdogMode", "WatchdogSec", "WorkingDirectory"},
	"Timer":    {"OnBootSec", "OnCalendar", "OnStartupSec", "OnUnitActiveSec", "Persistent", "Unit"},
	"Registry": {"RegistryChanged"},
	"EventLog": {"EventLogTrigger"},
	"Path":     {"PathChanged", "PathExists"},
	"Install":  {"WantedBy"},
}

// postBetaDirectives are recognized by this build but not by 0.2.1-beta, so a
// unit that uses one fails verification and reload on that release. Keep this
// list equal to the one in docs/RUNTIME-REFERENCE.md.
var postBetaDirectives = []string{
	"ExecStop", "ExecStopArg", "FormatVersion", "PathExistsAll",
	"ReadinessEndpoint", "ReadinessExpectedStatus", "ReadinessIntervalSec",
	"ReadinessMode", "ReadinessTimeoutSec", "RemainAfterExit", "RestartBackoff",
	"RestartMaxDelaySec", "WatchdogFailureThreshold", "WatchdogGraceSec",
	"WatchdogTimeoutSec", "WindowsCPUQuota", "WindowsCPUWeight",
}

func TestBetaDirectivesStayRecognized(t *testing.T) {
	t.Parallel()
	current := Directives()
	if !slices.Equal(slices.Sorted(maps.Keys(current)), slices.Sorted(maps.Keys(beta021Directives))) {
		t.Fatalf("sections changed: %v", slices.Sorted(maps.Keys(current)))
	}
	var added []string
	for section, names := range current {
		for _, name := range beta021Directives[section] {
			if !slices.Contains(names, name) {
				t.Errorf("[%s] %s from 0.2.1-beta is no longer recognized", section, name)
			}
		}
		for _, name := range names {
			if !slices.Contains(beta021Directives[section], name) {
				added = append(added, name)
			}
		}
	}
	slices.Sort(added)
	if !slices.Equal(added, postBetaDirectives) {
		t.Fatalf("directives added after 0.2.1-beta = %v, want %v", added, postBetaDirectives)
	}
}

// Unknown names are errors, never warnings, so a unit written for a newer
// build fails here instead of loading with the setting ignored.
func TestUnknownDirectivesAreErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, text string }{
		{"service", "[Service]\nExecStart=C:\\Apps\\worker.exe\nWorkingDirectory=C:\\Apps\nFutureStop=C:\\Apps\\stop.exe\n"},
		{"unit section", "[Unit]\nFutureFormat=3\n[Service]\nExecStart=C:\\Apps\\worker.exe\nWorkingDirectory=C:\\Apps\n"},
		{"unknown section", "[Future]\nSetting=1\n[Service]\nExecStart=C:\\Apps\\worker.exe\nWorkingDirectory=C:\\Apps\n"},
		{"case differs", "[Service]\nExecStart=C:\\Apps\\worker.exe\nWorkingDirectory=C:\\Apps\nexecstop=C:\\Apps\\stop.exe\n"},
	} {
		r := ParseUnit("future.service", tc.text)
		errs := r.Errors()
		if len(errs) == 0 || len(r.Warnings()) != 0 {
			t.Fatalf("%s: issues = %+v", tc.name, r.Issues)
		}
		if !strings.Contains(errs[0].Message, "unknown") {
			t.Fatalf("%s: %+v", tc.name, errs)
		}
	}
}
