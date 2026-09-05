# R0 baseline and initial qualification

September 5, 2026. Implementation: `15ccd9d`. R0.2 and R0.3 are complete work packages; R0 is not closed. No R1 runtime fix is claimed.

## Isolated tests

The user-manager integration tests previously used a temporary data directory but connected to the current user's production control pipe. An already-running manager could therefore receive fixture commands or make a failing child appear ready.

Each scenario now generates a random pipe under a separate test namespace, passes it explicitly to its test-binary subprocess, and dials only that endpoint. TestMain rejects missing/production endpoints and non-user-manager mode before daemon setup. Regression checks verify rejection before any data-directory creation. Existing temporary data directories, job ownership, child termination, and bounded waits remain in use. The logoff scenario verifies that its own pipe disappears.

Only the test binary reads the endpoint environment variable. The production listener factory continues to select the fixed user endpoint; no CLI or environment override is exposed in the installed daemon. A production build was checked for absence of the test-daemon environment controls. A repository test-source audit found no remaining direct calls to the default system/user control listener or dial functions.

Protocol tests retain production ACLs. Tests that require an elevated control-pipe connection now state that prerequisite explicitly. Three restricted-token fixtures require a non-SYSTEM user: removing Administrators membership from a SYSTEM token does not remove its LocalSystem identity. These fixtures run in the elevated-user lane instead of claiming to represent ordinary users under SYSTEM.

## Original review reproductions

Run explicitly on Windows from PowerShell:

```powershell
go test ./internal/manager -run '^TestReviewRepro' -count=1 -v -timeout 45s
```

These tests failed on the original review baseline as recorded below. Both now run unconditionally and pass after the R1 fixes; the opt-in environment gate has been removed.

| Reproduction | Baseline observation |
| --- | --- |
| Delete a live unit and reload | Process stays alive; status and stop return `not-found` |
| Replace a live unit with invalid configuration and reload | Parse error is reported; process stays alive; status and stop return `not-found` |
| Oneshot, four lines on stdout or stderr | Both controls pass |
| Oneshot, 5,000 lines on stdout or stderr | Both reach the five-second start timeout before output can drain |

The reload cases retain an independent process/job reference for cleanup even after the manager loses its lookup. Output cases record the helper PID and check that completion/timeout leaves no live child. All fixtures have temporary data directories and use direct manager APIs, with no connection to an installed manager. The observed runs completed cleanup without cleanup failures.

R1 retains these as required regressions and extends edge-case coverage.

R1 follow-up: reload ownership cases pass, including status/log/stop access, recreation without replacing the live process, and refusal to relaunch after stopping an unavailable unit. Delayed launch/stop regressions also pass. Oneshot completion now waits in the manager after output attachment, and the output cases verify all 5,000 lines from each stream. Real-process timeout and explicit-stop tests confirm that interrupted oneshots exit. The table above records historical defects; termination-failure injection and bounded capture remain separate R1 work.

## Qualification evidence

Toolchain used for the evidence below: Go 1.27.0, Windows amd64. Subsequent compiler/action pinning and updated-dependency results are recorded separately in [BUILDING.md](BUILDING.md).

- Local `go vet ./...` passed.
- Local `go test -race -parallel 1 ./... -timeout 180s` passed under the ordinary development token. Elevated-only cases and known failing reproductions were explicitly skipped; this is not full privileged coverage.
- After the final identity-prerequisite changes, `go test -race ./internal/protocol -count=1 -timeout 90s` passed; the endpoint guard was also rerun successfully.
- The opt-in reproduction run was repeated and produced the failure signatures above, with both small-output controls passing.
- The compiled protocol test executable was run in the Server 2025 Standard Evaluation Core lab, build 26100.32230. SYSTEM execution passed applicable tests and skipped exactly the three non-SYSTEM token fixtures. Elevated Administrator execution passed the full protocol package with no skips.
- The lab protocol executable SHA256 was `b340231d62b1cf8e3db206566a106c1309a0ebd6951841f303c8525a839a049c`, verified inside the guest against the locally built artifact. Lab test binaries were built without the race detector; local race results are separate evidence.

Private raw output and identity details are retained outside this repository. The lab guest was shut down after collection. No claim is made here for full-suite SYSTEM execution, a genuine standard-user interactive session, current patch compliance, MSI servicing, or a reproducible CI controller.

## Remaining R0 work

Pinned builds are qualified in [BUILDING.md](BUILDING.md), and the [packaging spike](PACKAGING-SPIKE.md) is complete. Turn supervised VM bring-up into a repeatable controller with isolated networking and explicit identity lanes. R0 closure still requires all milestone exit gates and maintainer acceptance.
