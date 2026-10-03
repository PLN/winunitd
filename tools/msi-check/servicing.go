package main

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/PLN/winunitd/internal/protocol"
	"github.com/PLN/winunitd/internal/runtime"
	"github.com/PLN/winunitd/internal/servicing"
)

// msiServiceControlWait is the stock Windows Installer wait documented for
// the ServiceControl table. The product helper must outlast that wait.
// https://learn.microsoft.com/en-us/windows/win32/msi/servicecontrol-table
const msiServiceControlWait = 30 * time.Second

// serviceStopBudget is how long repair, upgrade, and uninstall wait for
// SCM stop and process exit after quiesce. It matches the daemon
// preshutdown window and is longer than msiServiceControlWait.
// ServiceControl remains a backstop on a service this helper already stopped.
var serviceStopBudget = runtime.PreshutdownTimeout

// quiesceBudget is the maintenance RPC deadline used before that stop.
// A missing or failed quiesce aborts replacement; it is not success.
var quiesceBudget = time.Duration(protocol.MaxMaintenanceTimeoutMS) * time.Millisecond

var serviceTokenPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func validServiceToken(token string) bool {
	return serviceTokenPattern.MatchString(token)
}

// planServiceStop reports whether a running manager must be quiesced before
// the SCM stop. Any other state aborts so files are not replaced mid-transition.
func planServiceStop(state string) (quiesce bool, err error) {
	switch state {
	case "running":
		return true, nil
	case "stopped":
		return false, nil
	default:
		return false, fmt.Errorf("abort replacement: service is in transition")
	}
}

// requireQuiesced accepts only an explicit quiesced result.
func requireQuiesced(state string, callErr error) error {
	if callErr != nil {
		return fmt.Errorf("abort replacement: maintenance did not quiesce: %w", callErr)
	}
	if state != "quiesced" {
		return fmt.Errorf("abort replacement: maintenance did not quiesce: %s", state)
	}
	return nil
}

// floorPreflight refuses to install, repair or upgrade to a build below the
// compatibility floor persisted under dataDir, or when that record cannot be
// trusted. It runs before quiesce, stop or replacement, so a refusal leaves
// the installed manager and its workloads untouched. The helper is built from
// the same source in the same packaging run as the payload, so its own build
// identity stands for the package's. Uninstall admits no work and proceeds.
func floorPreflight(mode, dataDir string, build servicing.Build) error {
	if mode == modeUninstall {
		return nil
	}
	if hold := servicing.AdmissionHold(servicing.FloorPath(dataDir), build); hold != "" {
		return fmt.Errorf("preflight conflict: compatibility floor: %s", hold)
	}
	return nil
}

// replacementBlocked aborts file replacement while a service process or
// payload handle is still live.
func replacementBlocked(processLive bool, locked []string) error {
	if processLive {
		return fmt.Errorf("abort replacement: service process is still running")
	}
	if len(locked) > 0 {
		return fmt.Errorf("abort replacement: payload remains in use: %s", strings.Join(locked, ", "))
	}
	return nil
}
