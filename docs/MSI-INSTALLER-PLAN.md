# MSI installer plan

Status: implementation plan, not a shipped installer. Reviewed September 4, 2026 against the repository and the dev Hermes pilot.

Aligned September 5, 2026 with [ROADMAP.md](../ROADMAP.md), [Design v2](../DESIGN.md), and [R0–R8 milestones](MILESTONES.md). This document owns packaging details; the roadmap owns sequencing and release gates. The former M0–M5 sequence is mapped below for historical references.

## Delivery decision

Build a **machine-wide, x64 MSI** that installs the three binaries and one LocalSystem Windows service. The system service launches managers in users' own security contexts. Task Scheduler is a migration source, not a dependency of the installed product.

Use the maintained WiX Toolset with a pinned SDK/tool version and extensions. First milestone selects and records the exact supported release and its build dependencies. WiX documents an Open Source Maintenance Fee for revenue-generating use of its releases; record the applicable terms before adopting the build tooling. No tool subscription or signing service is purchased as part of this plan. See [WiX lifecycle and terms](https://docs.firegiant.com/wix/).

Initial qualification targets: Windows 11 x64 and Windows Server 2022/2025 x64, including Server Core. These are proposed test targets, not a claim of existing qualification. Add ARM64 only with its own native package and test coverage. No x86 or dual per-user/per-machine MSI in the first release. Installed binaries must run without Go, WiX, .NET, Python, or a package download on the destination machine.

## Installation contract

| Item | First-release behavior |
| --- | --- |
| Product | WinUnit Manager; publisher PLN; MIT license and dependency notices |
| Binaries | `ProgramFiles64Folder\winunitd\bin\`: `winunitd.exe`, `winctl.exe`, `winunit-notify.exe` |
| Documentation | Installed license, quick start, example units in a documentation directory; examples are not enabled |
| Machine data | `CommonAppDataFolder\winunitd\`: `units`, `enabled`, `journal`, `runtime`, `linger`, plus daemon diagnostics |
| User data | `%LOCALAPPDATA%\winunitd\`, created by the user manager in the owning user's context |
| Service | `winunitd`, display name `WinUnit Manager`, own process, LocalSystem, Automatic (Delayed Start) |
| Recovery | Restart after 1 second for first, second, and subsequent failures; include non-crash failures; retain the current infinite failure-reset policy |
| Shutdown | 180-second preshutdown setting; a tested bounded stop before installer file changes |
| PATH | Optional machine PATH entry for the binary directory, enabled by default; track ownership and preserve unrelated/pre-existing entries |
| Discovery | Apps & Features / Programs and Features entry with version, publisher, support URL, repair and uninstall |
| Network | No firewall exceptions; control remains local named pipes |
| User sessions | Reconcile already logged-on users at service startup; launch on later logon; recover eligible user-manager crashes |

Use fixed default binary/data locations for v1 to keep upgrades and ACL validation predictable. A manual installation with a custom base directory requires an explicit migration workflow; the MSI does not silently adopt or relocate it.

Install, repair, and uninstall must work through both normal Windows Installer UI and unattended `msiexec`, including deployment as SYSTEM. Proposed automation:

```powershell
msiexec /i winunitd-<release>-x64.msi /qn /norestart /L*v install.log
msiexec /fa winunitd-<release>-x64.msi /qn /norestart /L*v repair.log
msiexec /x winunitd-<release>-x64.msi /qn /norestart /L*v uninstall.log
```

Document success, reboot-required, concurrent-installation, and failure exit codes. Do not force a reboot. Do not launch an elevated shell or application from the finish page. Clearly disclose that upgrades and uninstall interrupt supervised workloads.

## Runtime work required before release

Packaging the current `winunitd install` command alone would conceal these gaps:

1. **User-manager crash recovery.** `internal/manager/userhost.go` starts instances on reconciliation/logon/linger operations but does not currently wait for a dead instance and automatically restart it. Add a bounded retry loop with backoff, fresh token acquisition, generation/ownership checks, and cancellation on logoff, disable-linger, and host shutdown. Never let the retry loop fight installer maintenance. This replaces the recovery role of the pilot task.
2. **Truthful startup readiness.** `internal/runtime/scm_windows.go` reports `Running` immediately after starting the daemon goroutine; the listener is created later in `cmd/winunitd/daemon.go`. Keep SCM in `StartPending` until initialization and control-pipe binding succeed. Define readiness independently of a workload's health, so an invalid user unit cannot make an otherwise functional manager impossible to install. Add a bounded installer health check that verifies the server identity and expected binary/protocol version.
3. **Daemon diagnostics.** Startup errors currently go to stderr, and SCM-launched user managers receive NUL output handles. Add bounded, ACL-protected daemon logs and a Windows Application event source for lifecycle/startup failures. Package a real message resource if required by the chosen event-source design; verify rendered event text. Keep unit stdout/stderr in the existing journal. Do not log credentials or environment values.
4. **Bounded maintenance shutdown.** Stop accepting new starts, disarm triggers, cancel restart loops, stop user managers and system units, and wait for owned processes/file handles to close. Apply one aggregate deadline, not an unbounded sum of per-unit deadlines. `stopUnitCtx` currently ignores its context and `UserHost.Close` kills user managers directly. R1–R4 replace this with truthful stop outcomes, configured ExecStop, forced fallback, and a maintenance barrier. R6 depends on that contract. Unspecialized workloads may still require forced termination; console/GUI signals remain deferred. Experimental MSI prototypes must disclose their current shutdown behavior.
5. **Reliable version/build identity.** Replace the hard-coded version constant with build-injected metadata and generate PE `VERSIONINFO` resources for every executable from the same release manifest. Keep the control-protocol version separate. Test service path quoting under Program Files and non-ASCII account/profile paths.
6. **Test isolation.** The user-manager integration test uses the real fixed user pipe and can contact an installed daemon. Give integration tests an isolated endpoint or make them fail/skip before any RPC if a non-test manager owns that endpoint. Installer acceptance runs in disposable VMs with no developer pilot.

The log-pagination, disable-error handling, and Windows helper deadlock fixes from the pilot are the starting baseline. The interactive-user pilot is not evidence that LocalSystem/WTS/S4U paths already work in production.

## MSI ownership, security, and custom actions

Author binaries, directories, registry identity, PATH, event source, and service registration as MSI components with stable identities and explicit key paths. Use `ServiceInstall` / `ServiceControl` for service ownership. Do **not** call the existing all-in-one `winunitd install` / `uninstall` commands from MSI: they update/create/start services outside the package's ownership and rollback model.

Use declarative WiX facilities where they meet the contract. Prototype advanced service settings early: WiX's core `ServiceConfig` documentation warns about its underlying MSI functionality, and the utility extension does not express every existing setting. Prefer a small, signed, narrowly scoped helper using `ChangeServiceConfig2` for settings that cannot be authored reliably. Query all settings back in tests; element names are not verification. See [WiX ServiceConfig caveat](https://docs.firegiant.com/wix/schema/wxs/serviceconfig/) and [utility recovery configuration](https://docs.firegiant.com/wix/schema/util/serviceconfig/).

The same packaging spike must prove a rollback-aware long-stop action. MSI's standard service-control wait is at most 30 seconds; winunitd's intended shutdown budget is 180 seconds. Quiesce and wait before `StopServices`/file replacement; leave standard actions operating on an already stopped service. A timeout aborts the transaction with diagnostics rather than replacing a still-running executable. See [Microsoft ServiceControl table](https://learn.microsoft.com/en-us/windows/win32/msi/servicecontrol-table).

Custom actions must use fixed operations and validated inputs, run deferred/elevated only when needed, and have rollback partners scheduled before mutation. Embed the helper in the package so old-product removal cannot delete the helper needed for rollback. Store transaction state in an administrator-owned location. Capture prior running state and advanced service settings; restore them on failure. Never execute arbitrary commands, unit files, or a helper from a user-writable directory as SYSTEM. No PowerShell or VBScript dependency for installed operation.

ACL requirements:

- Program Files payload: SYSTEM/Administrators can modify; ordinary users can read/execute.
- Machine unit definitions, enable records, runtime, linger state, and logs: SYSTEM/Administrators control access; no general-user write/create/delete permission. System unit files can lead to LocalSystem execution.
- User unit/data trees remain owned by the respective user. An elevated MSI must not write to whichever `%LOCALAPPDATA%` happens to be in its environment or walk all profiles to change their files.
- Inspect existing privileged directories and reject unsafe ownership, unexpected reparse points, or conflicting installations before starting the service. Do not recursively follow links to "repair" ACLs. Existing safe customer ACLs require a deliberate preservation policy.
- Named-pipe DACLs stay runtime-created and fail closed. MSI does not create a permanent pipe or widen the API's existing authorization rules.

## Versioning, repair, upgrades, and uninstall

Keep one stable x64 UpgradeCode, a new ProductCode for each major-upgrade release, a fresh PackageCode for each distinct MSI, and stable component GUIDs for unchanged installed resources. Record identifiers in source, not random build-time regeneration.

MSI compares only the first three numeric ProductVersion fields; prerelease suffixes and a fourth-field-only bump cannot order preview releases safely. Use an explicitly recorded monotonic installer version alongside the human release version, for example `0.1.0-alpha.1 -> 0.1.1`, `0.1.0-alpha.2 -> 0.1.2`, with the eventual stable installer also receiving a higher numeric version. Generate CLI strings, PE versions, package metadata, and release filenames from one manifest. Never publish different payloads with the same numeric installer version. Enforce MSI field bounds. See [Microsoft ProductVersion](https://learn.microsoft.com/en-us/windows/win32/msi/productversion).

Use major upgrades initially, block downgrades, and schedule removal of the old product after `InstallInitialize` so it participates in rollback. Keep same-version replacement disabled; reinstalling the same published MSI is a maintenance/repair operation. Do not start old and new managers concurrently. See [WiX major-upgrade scheduling](https://docs.firegiant.com/wix/schema/wxs/majorupgrade/).

For upgrade/repair: detect and validate ownership; preserve the existing data path, feature choices, and relevant service state; enter maintenance; stop all owned processes; replace package-owned files; restore configuration; start and verify as appropriate. Rollback must restore the previous binaries/configuration and resume a previously running manager. Restarting a manager after upgrade boots enabled units; restoration of manually started, non-enabled workloads is not promised in v1 and must be documented.

Keep mutable files outside file components. Repair must not overwrite units, reset enable links, or recreate user configuration. Uninstall removes service registration, package-owned binaries/metadata/event registration, and the PATH token added by this package. It **preserves machine and user units, journals, timer state, and linger records**, including during a major upgrade. No purge option in the first MSI. Document the retained data and the effect of reinstalling over existing enabled units. Data format changes must remain backward-readable for rollback or be separately transactional; packaging rollback alone does not reverse application data migrations.

## Migrating the Hermes pilot and manual installs

The MSI is generic; it must not contain the pilot user's SID, profile path, Hermes credentials, Python runtime, or SeaShell installation.

Provide a separate explicit migration command/script with discovery and dry-run output. For dev it will:

1. Export task definitions and snapshot the unit/enable configuration and source locations.
2. Verify target units under the user identity, retain Hermes's existing home, and preserve the WebUI guardian behavior.
3. Disable the pilot manager task and old Hermes triggers, then stop their owned processes so no fixed user-pipe collision or duplicate launcher remains.
4. Copy the pilot unit and enable records into the standard user data tree with conflict detection and an inverse operation. Keep logs in place or archive them without overwriting another journal.
5. Install/start the MSI-owned system service; verify that it launches the user's manager with the correct profile, environment, and network credentials, then check all three Hermes components.
6. Only mark migration successful after control ownership and application health checks pass. On failure, stop the new ownership path and restore the former tasks/configuration; retain backups.

An unrelated pre-existing SCM service named `winunitd`, unmanaged binary path, or incompatible pilot is a preflight conflict, not permission to overwrite it. Detect what can be established from a machine-wide install context; require the user-context migration step for per-user discovery. Linger remains explicit opt-in and needs LocalSystem qualification; MSI must not collect account passwords.

## Implementation sequence

| Roadmap ownership | Concrete deliverable | Exit gate |
| --- | --- | --- |
| R0.5 (former M0) | Pin WiX/SDK; record terms; prototype advanced service settings, long-stop, and rollback | Fixture service settings and injected rollback verified in a VM |
| R1–R5 (former M1 runtime work) | Ownership, lifecycle coordinator, compatibility, Windows identities, maintenance, diagnostics, isolated tests | All runtime milestone gates pass; no retry during stop/upgrade |
| R6.1 / R6.4 (former M2) | Package project, version metadata, files, ACLs, PATH, service/event source, quiet install, repair, uninstall | Fresh offline install/repair/uninstall; retained user state |
| R6.2–R6.5 (former M3) | Major upgrades, rollback, conflicts, explicit format and pilot migration | N-1 and failed-upgrade tests with running system/user workloads |
| R7 (former M5) | Migrate dev from Task Scheduler to MSI/SCM ownership | Reboot, logon/logoff, crash, repair, upgrade, rollback, and recorded soak |
| R8 (former M4) | Public readiness; SignPath or fallback; signed payload/MSI, attestations, checksums, immutable release | Provider integrated; downloaded signatures/provenance verified; all release gates pass |

Suggested repository layout: `packaging/wix/`, `packaging/resources/`, `scripts/build-release.ps1`, `tests/installer/`, `docs/INSTALLATION.md`, and a separate `.github/workflows/release.yml`. Keep host-specific pilot artifacts out of the repository. Create implementation issues from these milestones when work begins; this document itself does not claim they are implemented.

## Public release and signing roadmap

Keep the repository private while the runtime and installation basics are being proved. Before making it public, review repository history and release inputs for credentials and machine-specific artifacts, and finish the installation/recovery documentation. Changing visibility is a separate future action; this plan does not change it.

**Investigate SignPath Foundation first once the project is public.** Its free OSS signing program is the preferred option to evaluate, not an assumed entitlement or an enrollment already completed. The published conditions include an actively maintained, documented, already-released OSS project and verifiable reputation. Confirm how a clearly labeled unsigned preview can satisfy the existing-release requirement before making signed public installation releases a dependency. See the [application](https://signpath.org/apply) and [program conditions](https://signpath.org/terms).

For the SignPath evaluation, record licensing/dependency eligibility, verifiable CI artifact origin, enforced executable metadata, MFA, signing roles, and manual release approval. Prepare the required public code-signing/privacy policy. The Foundation certificate identifies **SignPath Foundation** as publisher; verify the resulting Windows publisher display and explain its relationship to winunitd and the package's PLN publisher metadata. Admission remains discretionary. These are future enrollment tasks, not claims of current compliance. See [SignPath conditions](https://signpath.org/terms).

Keep the release scripts independent of the chosen signing provider. If SignPath is unavailable or its conditions do not fit, evaluate Microsoft Artifact Signing for an eligible identity or another trusted CA's managed signing service. Authenticode does not require a Microsoft developer registration. As reviewed September 4, 2026, Microsoft's public-trust service supports EU organizations but individual applicants only in the US and Canada; recheck eligibility and pricing before selecting a provider. No purchase is planned yet. See [Microsoft signing options](https://learn.microsoft.com/en-us/windows/apps/package-and-deploy/code-signing-options).

R8 combines Authenticode with GitHub build-provenance attestations and immutable releases. A GitHub “Verified” commit badge does not sign the downloaded installer. Attestations establish build provenance; Authenticode establishes publisher identity and file integrity. Generate attestations and checksums for the **final signed bytes**, then publish the completed draft as an immutable release. Native GitHub artifact attestations are available for public repositories on Free/Pro/Team; private repositories require Enterprise Cloud. See [GitHub attestations](https://docs.github.com/en/actions/how-tos/secure-your-work/use-artifact-attestations/use-artifact-attestations) and [immutable releases](https://docs.github.com/en/code-security/concepts/supply-chain-security/immutable-releases).

Internal unsigned MSI testing can proceed before signing enrollment. Public installation releases still require the signing gates below. A valid signature does not guarantee that a new download avoids SmartScreen warnings; document that distinction in release guidance. See [Microsoft signing and reputation guidance](https://learn.microsoft.com/en-us/windows/apps/package-and-deploy/code-signing-options).

## Release and acceptance gates

Build release Go binaries with CGO disabled; race-test separately with CGO enabled. Record tool versions, source commit, dependencies, and release/installer version mapping. Build unsigned candidate MSIs on PRs and validate their tables/components with ICE validation; suppress only individually explained diagnostics. Installer tests use disposable elevated Windows VMs, with LocalSystem and genuine interactive sessions for the relevant paths.

Sign and timestamp executable/helper payloads **before** packaging, then sign and timestamp the MSI, and verify both embedded payloads and the package. Select a signing identity/provider during R8 and protect its credentials in a release-only environment. An unsigned artifact is an internal test candidate, not the public installation release. Document an offline-verification policy separately from online revocation/timestamp checks. See [Microsoft Authenticode timestamping](https://learn.microsoft.com/en-us/windows/win32/seccrypto/time-stamping-authenticode-signatures).

Publish the MSI, SHA-256 checksums, dependency/license notices, build manifest, and installation/recovery notes from an immutable release tag. Signing changes bytes, so record both unsigned build inputs and final signed-artifact hashes rather than claiming byte-identical signed builds. No automatic update agent in v1.

Required acceptance cases:

- Fresh GUI and quiet installs, SYSTEM deployment, offline installation, non-admin rejection/UAC, supported x64 desktop and server targets.
- Service identity, exact advanced SCM settings, quoted binary path, protected ACLs, correct PATH ownership, and no target-machine build/runtime prerequisites.
- Already logged-on users, multiple sessions/users, standard-user `winctl --user`, admin system control, server-owner verification, and denied cross-user access.
- SYSTEM/WTS startup, user-manager crash recovery, system-manager crash recovery, logout cancellation, and explicit linger qualification before advertising it.
- N-1 to N upgrade; repair of missing binaries; downgrade rejection; reinstall of the same package; preserved units/logs/timer state; no automatic activation of shipped examples.
- Injected failures after quiesce, old-product removal, file copy, configuration, and new-service startup; correct rollback and diagnostic logs for each.
- Stop exceeding 30 seconds but within the supported deadline; true deadline overrun; locked binaries; no process left using removed payload; no forced reboot.
- Uninstall while busy, preservation of both users' and machine data, removal of only owned PATH/registry/service resources, and a working reinstall over retained data.
- Conflicting manual/pilot installs, hostile/reparse-point data directories, and the Hermes migration/rollback with its authentication and health checks retained.

Packaging prototypes may be built during R0–R5. The installable internal MSI candidate requires R6 and its runtime prerequisites. Replacing dev's working pilot is R7; a public installation release requires R8 after pilot qualification. See [milestone gates and legacy mapping](MILESTONES.md).
