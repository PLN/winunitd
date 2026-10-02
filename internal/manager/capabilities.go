package manager

import (
	"runtime"
	"slices"

	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/unit"
	"github.com/PLN/winunitd/internal/version"
)

// Capabilities reports the build identity and the contracts this build
// enforces. It reads no unit, Windows, or file state and takes no lock. The
// answer describes the binary, so system and user managers of one build
// report the same set; linger and user-manager modes are exercised by the
// system manager.
func (m *Manager) Capabilities() *protocol.CapabilitiesResult {
	b := version.Build()
	scope := "system"
	if m.cfg.UserScope {
		scope = "user"
	}
	p := platformCapabilities()
	features := append([]string{protocol.FeatureRestartBackoff}, p.features...)
	slices.Sort(features)
	return &protocol.CapabilitiesResult{
		Product:  "winunitd",
		Version:  b.Version,
		Commit:   b.Commit,
		Modified: b.Modified,
		Go:       b.Go,
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
		Scope:    scope,
		Protocol: protocol.ProtocolCapabilities{
			Name:    protocol.Name,
			Version: protocol.Version,
			Methods: slices.Clone(protocol.Methods),
		},
		FormatVersions:   unit.FormatVersions(),
		Features:         features,
		JobLimits:        nonNil(p.jobLimits),
		UserManagerModes: nonNil(p.userModes),
		Directives:       unit.Directives(),
	}
}

// buildReason identifies the build on the durable daemon.open record, so a
// change of build across repair, upgrade, or rollback stays visible. It uses
// the existing reason field: readers of any daemon-log version accept it, and
// the Windows event text for daemon.open does not include it.
func buildReason(b version.BuildInfo) string {
	reason := "version " + b.Version
	if b.Commit != "" {
		reason += " commit " + b.Commit
	}
	if b.Modified != nil && *b.Modified {
		reason += " modified"
	}
	return reason
}

// jobLimitDirectives are the [Service] directives that
// runtime.JobLimitsFromSpec applies to a managed workload's Job Object.
// Format rules still select which of them a file may use.
var jobLimitDirectives = []string{
	"CPUQuota", "CPUWeight", "IoPriority", "MemoryMax", "PriorityClass",
	"ProcessLimit", "WindowsCPUQuota", "WindowsCPUWeight",
}

// platformCapability is the part of the report that depends on the native
// runtime. Non-Windows builds use stub launchers and report none of it.
type platformCapability struct {
	features  []string
	jobLimits []string
	userModes []string
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return slices.Clone(s)
}
