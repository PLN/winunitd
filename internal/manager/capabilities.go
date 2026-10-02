package manager

import (
	"context"
	"encoding/json"
	"runtime"
	"slices"

	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/unit"
	"github.com/PLN/winunitd/internal/version"
)

// controlMethods are implemented only by Control, the system endpoint. A
// Manager serving a user pipe answers them method-not-found.
var controlMethods = []string{protocol.MethodEnableLinger, protocol.MethodDisableLinger, protocol.MethodMaintenance}

// Capabilities reports the build identity and what a Manager endpoint
// enforces: its own methods, workload features and job limits. It reads no
// unit, Windows, or file state and takes no lock. Control adds the system
// endpoint's methods, linger and user-manager launch.
func (m *Manager) Capabilities() *protocol.CapabilitiesResult {
	b := version.Build()
	scope := "system"
	if m.cfg.UserScope {
		scope = "user"
	}
	p := platformCapabilities()
	features := append([]string{protocol.FeatureRestartBackoff}, p.workloadFeatures...)
	slices.Sort(features)
	methods := slices.DeleteFunc(slices.Clone(protocol.Methods), func(name string) bool {
		return slices.Contains(controlMethods, name)
	})
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
			Methods: methods,
		},
		FormatVersions:               unit.FormatVersions(),
		Features:                     features,
		JobLimits:                    nonNil(p.jobLimits),
		UserManagerModes:             []string{},
		ExperimentalUserManagerModes: []string{},
		Directives:                   unit.Directives(),
	}
}

// Capabilities reports the system endpoint: every control method, plus
// linger and user-manager launch when a user host is configured.
func (c *Control) Capabilities() *protocol.CapabilitiesResult {
	out := c.Units.Capabilities()
	out.Protocol.Methods = slices.Clone(protocol.Methods)
	if c.Users != nil {
		p := platformCapabilities()
		out.Features = append(out.Features, p.systemFeatures...)
		slices.Sort(out.Features)
		out.UserManagerModes = nonNil(p.userModes)
		out.ExperimentalUserManagerModes = nonNil(p.experimentalUserModes)
	}
	return out
}

func (c *Control) handleCapabilities(_ context.Context, params json.RawMessage) (any, error) {
	var p struct{}
	if err := protocol.DecodeParams(params, &p); err != nil {
		return nil, err
	}
	return c.Capabilities(), nil
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
// runtime.JobLimitsFromSpec applies to a managed process service's Job
// Object. IoPriority is a process setting on the main process and is not
// listed. Format rules still select which of them a file may use.
var jobLimitDirectives = []string{
	"CPUQuota", "CPUWeight", "MemoryMax", "PriorityClass",
	"ProcessLimit", "WindowsCPUQuota", "WindowsCPUWeight",
}

// platformCapability is the part of the report that depends on the native
// runtime. Non-Windows builds use stub launchers and report none of it.
type platformCapability struct {
	workloadFeatures      []string
	jobLimits             []string
	systemFeatures        []string
	userModes             []string
	experimentalUserModes []string
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return slices.Clone(s)
}
