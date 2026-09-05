# Revision 2 implementation milestones

September 5, 2026. Implements [ROADMAP.md](../ROADMAP.md) and [Design v2](../DESIGN.md). Every milestone below is **planned**; documentation adoption does not complete implementation. Work-package IDs are suitable issue-title prefixes. This file defines repository milestones; no GitHub milestone objects or implementation issues have been created by this documentation change.

## Acceptance rules

For each completed milestone, record implementation commits, automated results, applicable Windows build and execution identity, manual acceptance procedure/results, and remaining limitations. A skipped SYSTEM/VM test is missing evidence. Redact host-specific data. The maintainer accepts closure against the exit gate; individual checked tasks do not override it.

Do not change existing pilot ownership merely to run tests. Use isolated endpoints, process jobs, data directories, and disposable VMs. Runtime fixes include permanent regressions. Documentation-only changes require link/consistency checks rather than a runtime test rerun.

## R0 — Reproducible baseline and qualification harness

Status: planned. Dependencies: none. Outcome: a safe, repeatable way to prove subsequent work.

- [ ] **R0.1 Toolchain:** pin a supported patched Go compiler separately from minimum language compatibility; record local/CI/build metadata; pin action revisions and define update/vulnerability-check policy.
- [ ] **R0.2 Isolation:** prevent integration tests from connecting to the real system/user manager; give fixtures dedicated endpoint/data namespaces and bounded process cleanup.
- [ ] **R0.3 Review reproductions:** retain opt-in failing reproductions for reload ownership and large-output oneshots with small-output controls. Record baseline failure signatures; R1 converts them into passing required regressions. Do not disguise them as passing functionality tests.
- [ ] **R0.4 Windows harness:** implement the [qualification lab plan](TEST-LAB.md) for disposable VM setup and evidence collection for SCM, SYSTEM, standard users, sessions, reboot, and installer failure injection. A first Server 2025 Core evaluation deployment and supervised SYSTEM/SCM/reboot smoke passed with source and artifact identities recorded. Reproducible baselines, isolated networking, controller, CI integration, and broader scenarios remain pending.
- [ ] **R0.5 Packaging spike:** pin WiX/SDK candidates and applicable terms; prototype advanced service settings and rollback-aware long-stop behavior using a fixture service. Record helper strategy and observed MSI constraints.

Exit gate: supported builds and existing isolated test lanes pass; the two review defects are reproducible without touching Hermes; VM provisioning/smoke and packaging-spike evidence can be repeated. Runtime defects remain open for R1. This milestone does not qualify the application for installation.

## R1 — Ownership, output, and failure containment

Status: planned. Dependencies: R0. Outcome: urgent correctness fixes in the existing implementation before structural refactoring.

- [ ] **R1.1 Reload:** retain live runtime/invocation records when files disappear or become invalid; keep status/log/stop access; prohibit duplicate replacement. Preserve the last accepted configuration on invalid replacement.
- [ ] **R1.2 Activation:** separate process creation from oneshot completion; drain stdout/stderr before waiting for readiness/exit; guarantee cleanup on timeout and launch failure.
- [ ] **R1.3 Stop results:** propagate stop/kill/wait failures; retain ownership until termination is confirmed; reject relaunch when termination is uncertain. Keep current forced-stop behavior explicitly documented until R3.
- [ ] **R1.4 Capture bounds:** cap/chunk unfinished lines and bound buffering; define behavior for slow/full storage without blocking child output indefinitely.

Exit gate: real-process tests cover deletion, invalid replacement, recreation while an old invocation lives, large stdout/stderr, no-newline output, exit-before-attach, and failed termination. The previously reproduced failures now pass required regression tests. Race tests and cleanup assertions pass; no owned process becomes unreachable through status/stop and no ambiguous termination is reported as successful.

## R2 — Authoritative lifecycle coordinator

Status: planned. Dependencies: R1. Outcome: one state owner, concurrent I/O, explicit operation identity.

- [ ] **R2.1 Records/events:** implement immutable configuration revisions, stable runtime records, invocation/operation IDs, generations, typed completion events, and immutable status snapshots.
- [ ] **R2.2 Coordinator:** migrate start/stop/restart, process exit, notify, watchdog, timer/native trigger activation, reload, and user-host lifecycle decisions. Remove direct worker state writes and transaction-result overwrites.
- [ ] **R2.3 Scheduling:** bound admission/workers, preserve completion delivery under overload, coalesce redundant starts, and ensure stop/maintenance precedence. Keep blocking I/O outside the coordinator.
- [ ] **R2.4 Operations:** retain accepted operations across client disconnect; expose queryable outcomes; apply internal deadlines and explicit cancellation; version protocol changes with client compatibility tests.
- [ ] **R2.5 Interleavings:** build deterministic operation-sequence tests with fake clocks and delayed workers; migrate existing tests rather than replacing them with implementation-mirroring tests.

Exit gate: each invariant in Design v2 §3 has a test/evidence mapping. Start/stop/restart/reload/exit/timeout/trigger permutations, late successful launches, stale probes, overload, client disconnect, and shutdown during launch preserve ownership and ordering. A source audit finds no second lifecycle authority. Independent units still perform I/O concurrently.

## R3 — Unit semantics, compatibility, and application health

Status: planned. Dependencies: R2. Outcome: an explicit v2 behavior contract and controlled migration from alpha semantics.

- [ ] **R3.1 Reference/migration:** publish `docs/UNIT-REFERENCE.md` with format marker, exact directive names/defaults, accepted/rejected features, argv grammar, and a compatibility table. Add a dry-run converter for legacy PathExists, CPU policy, and oneshot behavior; never silently rewrite unit files.
- [ ] **R3.2 Dependencies:** implement/test BindsTo on unexpected disappearance and PartOf stop/restart participation, including reverse members absent from the root's Wants. Document Requires versus ordering and partial transaction results.
- [ ] **R3.3 Services/stop:** implement default completed/inactive oneshots and explicit RemainAfterExit; add ExecStop under the workload identity with tracked helper ownership, graceful deadline, forced fallback, and termination confirmation. Console/GUI signals remain deferred.
- [ ] **R3.4 Readiness/liveness:** separate startup readiness from health/recovery; provide explicit probe/grace/threshold policies and bounded restart backoff. Tag results with invocation identity; preserve rate limits for timer/watch activations.
- [ ] **R3.5 Native features:** expose proxy ownership/capabilities, define Windows CPU names/scales, and test retained path/registry/eventlog features against the reference. Unqualified behavior remains rejected or marked experimental.

Exit gate: conformance tests cover startup, explicit stop, restart, natural exit, dependency skip/failure, and stale observations. Legacy fixtures preserve effective behavior or produce actionable migration errors; v2 files follow v2 semantics. Cooperative stop, hanging stop helper, forced fallback, and expired aggregate deadline all report accurate outcomes. Readiness gates dependents; transient health failures follow configured thresholds.

## R4 — Windows service and user-manager qualification

Status: planned. Dependencies: R2, R3. Outcome: real Windows identities and session transitions behave as designed.

- [ ] **R4.1 Launch context:** select and implement a SYSTEM-to-user process launch mechanism that obeys cross-session handle rules. Obtain the target user's profile/environment/known folders; exclude arbitrary broker environment; track profile and token lifetime.
- [ ] **R4.2 User host:** define interactive-user admission; reconcile sessions and manager exits; acquire fresh tokens on bounded recovery; cancel in-flight launch/recovery on logoff, disable-linger, and shutdown.
- [ ] **R4.3 SCM/maintenance:** report readiness after listener/coordinator initialization; keep diagnostics available when workload configuration is invalid; implement a global maintenance barrier with one deadline across user managers and system units.
- [ ] **R4.4 Security:** verify pipe ownership/DACL/token checks, UAC-filtered users, cross-user rejection, protected privileged paths, reparse-point handling, and no unintended inherited handles.
- [ ] **R4.5 Linger modes:** qualify explicit headless S4U behavior and its credential limitations. Any unqualified optional credential-store mode remains disabled/experimental and is excluded from supported release claims.

Exit gate: disposable VM evidence includes genuine LocalSystem session-0 launch into a standard user's session, existing-session reconciliation, multiple sessions for one SID, separate users, manager crash, rapid logon/logoff, shutdown during launch, profile/environment cases, and explicit linger behavior. One manager per SID and no post-stop resurrection hold. Unsupported modes fail visibly. No hosted-admin-only evidence is accepted as a substitute.

## R5 — Durable timers and operational diagnostics

Status: planned. Dependencies: R2, R3. Outcome: bounded resource use and visible recovery/storage failures.

- [ ] **R5.1 Timer persistence:** implement atomic validated state replacement, pending/result activation records, corruption diagnostics, durable-write failure suspension, and explicit state-format migration.
- [ ] **R5.2 Delivery policy:** coalesce missed calendar occurrences; retry interrupted pending activation with documented duplicate possibility. Test clock jumps, DST, suspend/resume, boot/startup origins, and overlap with a running service.
- [ ] **R5.3 Journaling:** qualify line/queue/total-memory/retention bounds; expose loss counters, continuation records, storage degradation, and backward-compatible journal reading. Keep stream separate from severity.
- [ ] **R5.4 Diagnostics/API:** provide durable daemon diagnostics and Windows event resources; expose operation/invocation/configuration identity, lifecycle/health/load state, last errors, restart budget, and timer/storage state in bounded responses.
- [ ] **R5.5 Stress/fault tests:** combine burst triggers, slow readers, full disk, interrupted writes, large output, and repeated process failures under documented resource budgets.

Exit gate: fault injection never silently treats corrupt persistence as an empty successful state; pending activations recover according to the documented policy. Bounded logging continues to drain children under storage failure, loss is observable, and control remains responsive under stress. Artifact evidence states numeric budgets and measured results. Event messages render on a clean Windows installation.

## R6 — Serviceable internal MSI

Status: planned. Dependencies: R3, R4, R5; packaging spike from R0. Outcome: an internal installer whose install/upgrade/rollback behavior is proven.

- [ ] **R6.1 Package:** implement the [installer contract](MSI-INSTALLER-PLAN.md), stable component/upgrade identity, version/PE metadata, protected files/data, optional owned PATH entry, service/event registration, quiet UI, and no destination build prerequisites.
- [ ] **R6.2 Maintenance/rollback:** quiesce before replacing files; test stop beyond MSI's standard wait; preserve prior service/configuration state and restore it on injected failures. Abort replacement when processes or handles remain owned/live.
- [ ] **R6.3 Data/compatibility:** preserve mutable data across repair/uninstall/upgrade; require explicit unit/state migration and keep rollback readers compatible. Detect conflicting manual installations and unsafe directories.
- [ ] **R6.4 Acceptance:** execute clean GUI/quiet/SYSTEM/offline install, repair, uninstall, reinstall, downgrade rejection, N-1 upgrade, locked-file/reboot-required, and rollback matrix on every claimed platform.
- [ ] **R6.5 Migration tool:** implement discovery, task/configuration backup, dry run, conflict reporting, owner handoff, health verification, and inverse operations using a VM pilot fixture.

Exit gate: publish a versioned internal MSI candidate, checksums/build manifest, redacted VM evidence, installation/recovery instructions, and documented limitations. No MSI action depends on user-writable privileged executables or silently converts personal Hermes state. Signing enrollment is not a prerequisite for this internal gate.

## R7 — Hermes pilot replacement and soak

Status: planned. Dependencies: R6. Outcome: the dev machine's real workload demonstrates the installation/runtime contract.

- [ ] **R7.1 Preflight:** inventory actual tasks/processes, exported configuration, owner identity, health endpoints, authentication, ports, and rollback assets. Keep host-specific values outside source control.
- [ ] **R7.2 Handoff:** stop/disable competing launchers, migrate configuration explicitly, install the MSI service, and verify one owner for each gateway/dashboard/web interface process tree.
- [ ] **R7.3 Exercise:** test real reboot, logon/logoff, user-manager and workload crashes, component readiness, logs, graceful/forced stop, repair, upgrade, and return to the previous ownership path.
- [ ] **R7.4 Soak:** observe at least seven consecutive days of normal use after the last lifecycle-affecting fix, including the transitions above. Record unexpected restarts, duplicate processes, missing logs, and resource trends. Investigate anomalies and repeat affected checks; restart the soak after a lifecycle fix.

Exit gate: all three components meet their health/authentication checks under MSI/SCM ownership; recovery and rollback were actually exercised; no unexplained ownership loss, duplicates, or unbounded resource growth during the recorded soak. An ordinary application/upstream outage is classified separately from supervisor failure. Restore the previous pilot if the handoff fails.

## R8 — Public source and verified installation release

Status: planned. Dependencies for release: R7. Public-source/provider preparation may begin earlier under roadmap policy.

- [ ] **R8.1 Public readiness:** review source history and release inputs for credentials, personal names, real machine names, internal addresses, and host artifacts; sanitize historical content before making the repository public. Complete behavior, installation, recovery, support, license, and security-reporting documentation. Record qualified platforms/modes. Source visibility changes remain a separate action.
- [ ] **R8.2 Provider:** investigate/apply to SignPath Foundation once eligible; record terms, roles, MFA, manual signing approval, required policy, verifiable build origin, and Windows publisher display. If not accepted, select an eligible alternative. Do not claim acceptance before enrollment succeeds.
- [ ] **R8.3 Pipeline:** build with pinned tools/actions in a protected release workflow; sign/timestamp payloads, assemble/sign MSI, verify payload/package signatures, then hash and attest final bytes. Protect signing authority from untrusted PR execution.
- [ ] **R8.4 Publish/verify:** assemble complete draft assets, publish an immutable release, and independently verify the downloaded MSI, attestations, checksums, and clean-machine installation. Document reputation warnings without promising they disappear.

Exit gate: a public installation release and its verification evidence exist, the selected provider's requirements are met, and downloads match final tested signed artifacts. Provider rejection blocks only this public-signing gate, not internal qualification; fallback remains available. A signed MSI does not waive any earlier milestone.

## Legacy installer milestone mapping

The former M0–M5 sequence was packaging-focused. Use R0–R8 for all new tracking; old references map as follows:

| Former ID | Revision 2 ownership |
| --- | --- |
| M0 packaging spike | R0.5 |
| M1 runtime readiness | R1–R5; version/PE metadata in R6.1 |
| M2 first MSI | R6.1 and R6.4 |
| M3 servicing/migration | R6.2–R6.5 |
| M4 release pipeline | R8 |
| M5 pilot replacement | R7 |

Pilot qualification now precedes public release acceptance. Package prototypes can still be built earlier without being declared installation-ready.
