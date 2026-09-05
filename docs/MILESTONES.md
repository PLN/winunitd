# Revision 2 implementation milestones

September 5, 2026. Implements [ROADMAP.md](../ROADMAP.md) and [Design v2](../DESIGN.md). R0 is **in progress**; later milestones remain planned. Documentation adoption does not complete implementation. Work-package IDs are suitable issue-title prefixes. This file defines repository milestones; no GitHub milestone objects or implementation issues have been created by this documentation change.

## Acceptance rules

For each completed milestone, record implementation commits, automated results, applicable Windows build and execution identity, manual acceptance procedure/results, and remaining limitations. A skipped SYSTEM/VM test is missing evidence. Redact host-specific data. The maintainer accepts closure against the exit gate; individual checked tasks do not override it.

Do not change existing pilot ownership merely to run tests. Use isolated endpoints, process jobs, data directories, and disposable VMs. Runtime fixes include permanent regressions. Documentation-only changes require link/consistency checks rather than a runtime test rerun.

## R0 — Reproducible baseline and qualification harness

Status: in progress. Dependencies: none. Outcome: a safe, repeatable way to prove subsequent work. Initial implementation and qualification evidence: [R0 baseline](R0-BASELINE.md).

- [x] **R0.1 Toolchain:** implemented in `7b788e9` and `40858a8`; local checks and all CI lanes pass, and native Windows/Linux cross-build artifact hashes match. Compiler/action pins, artifact metadata, and vulnerability/update policy are recorded in [build policy and evidence](BUILDING.md).
- [x] **R0.2 Isolation:** prevent integration tests from connecting to the real system/user manager; give fixtures dedicated endpoint/data namespaces and bounded process cleanup. Implemented in `15ccd9d`; test-daemon endpoint guards and isolated user-manager scenarios pass.
- [x] **R0.3 Review reproductions:** retain opt-in failing reproductions for reload ownership and large-output oneshots with small-output controls. Recorded in `15ccd9d`; deletion/invalid replacement and large stdout/stderr reproduce the defects, small-output controls pass, independent cleanup completes. R1 converts them into passing required regressions.
- [ ] **R0.4 Windows harness:** implement the [qualification lab plan](TEST-LAB.md) for disposable VM setup and evidence collection for SCM, SYSTEM, standard users, sessions, reboot, and installer failure injection. Fresh Server Core, Enterprise LTSC, and Enterprise evaluation guests passed controller-driven SYSTEM/SCM/reboot smoke using exact CI artifacts. Trusted Gitea artifact dispatch/download/guest consumption and guarded guest/disk retirement passed; ownership/expiry reconciliation is implemented. Server maintenance converged; LTSC is accepted for development testing with the repeatedly offered Windows Security app update recorded as a non-blocking known issue. Media preparation remains supervised; broader identity/session scenarios and unattended operation remain pending.
- [x] **R0.5 Packaging spike:** implemented in `ea7fb35`; clean-commit SYSTEM qualification passed install, repair, running/stopped rollback after old-product removal, stop-deadline failure, slow upgrade, and uninstall. Tooling terms, native MSI limitations, helper strategy, exact artifacts, and remaining production requirements are recorded in the [packaging spike](PACKAGING-SPIKE.md).

Exit gate: supported builds and existing isolated test lanes pass; the two review defects are reproducible without touching Hermes; VM provisioning/smoke and packaging-spike evidence can be repeated. Runtime defects remain open for R1. This milestone does not qualify the application for installation.

## R1 — Ownership, output, and failure containment

Status: in progress alongside remaining supervised R0 qualification. Dependencies for closure: R0. Outcome: urgent correctness fixes in the existing implementation before structural refactoring.

- [ ] **R1.1 Reload:** process ownership fix implemented; live processes and in-flight start/stop calls retain their records and last accepted configuration when files disappear or become invalid. Status reports `LoadState=unavailable`; status/log/stop remain accessible, automatic restarts are suppressed, and a new process requires valid reloaded configuration. Required Windows regressions cover deletion, invalid replacement, recreation without duplicate launch, and stop; a controlled delayed-launch test covers reload before process creation returns. Local full race suite and vet pass. Native proxy/trigger coverage and CI confirmation remain before closing this work package.
- [ ] **R1.2 Activation:** process creation now returns before oneshot completion; the manager attaches output and waits for exit, draining final output before closing handles. Required Windows tests verify all 5,000 stdout/stderr lines, timeout cleanup, and explicit stop of a waiting oneshot. Existing completed-oneshot active state is preserved until R3. Local race suite and vet pass; launch/termination failure ownership still needs fault-injection coverage with R1.3.
- [ ] **R1.3 Stop results:** explicit stop now propagates failure, retains the process reference, detects a still-live process despite reported success, and blocks replacement starts until a successful stop retry. Failed-state cleanup cannot discard that uncertain process. The Windows adapter preserves handles on kill/wait failure. Fault-injection tests cover both adapter failures and manager ownership/retry behavior. Shared asynchronous teardown paths still need review before closure. Current stop remains forced Job Object termination until R3.
- [ ] **R1.4 Capture bounds:** stdout/stderr capture emits UTF-8 fragments of at most 64 KiB before newline/EOF, with continuation/partial metadata in journal v3 and the logs API. Queued message data is capped at 4 MiB per invocation and 16 MiB per store, with a 16,384-fragment queue cap. Capture keeps draining on overflow; unit status exposes drops and storage errors. Capture/sync waits and journal close have deadlines; at most four sync workers can remain blocked. Tests cover Unicode reconstruction, old journal readers, blocked writes/sync, overflow, failed writes, and recovery after a close timeout. Remaining qualification includes real-process storage faults, queue fairness under competing units, read-path stalls, total lifecycle admission bounds, and shutdown integration; this is not yet a milestone closure.

Additional R1 finding: notification tests exposed an early-disconnect transport race and an idle-client shutdown hang. The notify helper now waits for a server-acceptance banner before writing; canceled listeners close accepted clients. The single-send Windows manager test passes 100 race-enabled repetitions, with portable acceptance/cancellation regressions. This alpha transport change requires upgrading the helper and daemon together; it does not acknowledge readiness or replace the invocation/authentication work in R2/R3.

R1.1 native proxy follow-up: active or unresolved SCM/task proxies now retain their last configuration after deletion or invalid replacement, keeping status and stop routing available without a process handle. Portable adapter regressions verify both proxy types and actual stop dispatch before the missing record is retired on a later reload. The earlier runtime/notify fixes passed all GitHub CI lanes at `a1f46c0`. Trigger coverage and valid configuration changes during an invocation remain open.

R1.1 trigger follow-up: shared watch installation rejects late opens after configuration loss or manager close and closes the newly opened handles. Unavailable retained timers/hubs are disarmed and cannot activate companions; late timer arming is rejected. Controlled path-open and timer callback interleavings pass, along with the full manager race suite. Valid configuration replacement while an invocation runs still belongs to immutable revision work in R2.

R1.3 failed-state cleanup follow-up: asynchronous reaping retains the process reference, serializes termination with lifecycle operations, blocks replacement starts until confirmed cleanup, and exposes cleanup failures for explicit stop retry. A main-process exit cannot discard a record already marked uncertain. Fault injection covers retained ownership after failure and later main exit, successful retry, and replacement after successful cleanup. Full local race tests and vet pass. Ordinary exit teardown, late launch disposal, and manager shutdown still require the broader ownership audit.

R1.3 exit cleanup follow-up: the ordinary exit watcher and a start that encounters an exited but unreaped process now retain ownership until adapter cleanup succeeds. Failure blocks restart/replacement and remains available for explicit stop retry. The operation lock is released before restart delay/relaunch; existing real Windows restart tests cover that boundary. Targeted fault injection and the full local race suite pass. Late launch disposal, pending stop completions, complete descendant-exit confirmation, and manager shutdown remain open. The bounded journal implementation passed every GitHub CI lane at `ee64959`.

R1.3 pending stop follow-up: manager stop retries now join an outstanding adapter call after caller timeout, preventing concurrent termination/handle cleanup and repeated blocked stop goroutines for the same process. A controlled blocked adapter test covers repeated timeouts, one adapter invocation, and recovery. Direct legacy cleanup paths still need migration to this shared stop path.

R1.3 health failure follow-up: watchdog and readiness-timeout cleanup now use shared stop attempts, preserve unresolved process ownership, and suppress restart on cleanup failure. Watchdog cleanup closes the prior notify/watchdog controls and releases the operation lock before restarting. Fault-injection regressions and the full local race suite pass. Late-launch disposal and shutdown still require the remaining ownership work.

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
