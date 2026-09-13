package manager

import (
	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/unit"
)

// Copy the same captured definition used to route native stop and observation.
// No adapter call or mutable shared capability slice enters an accepted view.
func nativeProxyStatus(svc *unit.ServiceSpec) *protocol.NativeProxyStatus {
	var target, owner string
	switch svc.Type {
	case unit.TypeSCM:
		target, owner = svc.ServiceName, "windows-scm"
	case unit.TypeScheduledTask:
		target, owner = svc.TaskName, "windows-task-scheduler"
	default:
		return nil
	}
	return &protocol.NativeProxyStatus{
		Kind: string(svc.Type), Target: target, Owner: owner,
		Capabilities: []string{"query-state", "request-start", "request-stop", "request-restart"},
	}
}
