# Revision 2 implementation milestones

September 6, 2026. Implements [ROADMAP.md](../ROADMAP.md) and [Design v2](../DESIGN.md). R0, R1, and the initial R2 ownership work are **in progress**; later milestones remain planned. Documentation adoption does not complete implementation. Work-package IDs are suitable issue-title prefixes. This file defines repository milestones; no GitHub milestone objects or implementation issues have been created by this documentation change.

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

- [ ] **R1.1 Reload:** live processes, in-flight operations, and active/unresolved SCM/task proxies retain their records and last accepted configuration. Accepted removals report `LoadState=unavailable`; status/log/stop remain accessible, restarts are suppressed, and new launches require valid configuration. R2 now rejects invalid/cyclic candidates as a whole, retaining the previous loaded definitions and start eligibility. Required Windows deletion/recreation regressions and portable proxy/trigger interleavings pass. Late watch installation is rejected and its handles closed; unavailable timers/hubs cannot activate companions. Configuration revision identity remains R2 work.
- [ ] **R1.2 Activation:** process creation returns before oneshot completion; output capture attaches before waiting for exit. Required Windows tests verify all 5,000 stdout/stderr lines, no-newline fragments, startup timeout cleanup, and explicit stop while waiting. Existing completed-oneshot active state is preserved until R3. Post-creation unit launch failures preserve process/thread handles; if cleanup fails, the launcher returns the process with its error and the manager retains it for status/stop retry. Broader failure qualification remains coupled to R1.3.
- [ ] **R1.3 Stop results:** retain process, job, and watcher ownership until cleanup succeeds; preserve status/stop retry and block replacement while cleanup is unresolved. Process/native/watch stop retries join pending adapter calls. Shutdown rejects new starts, accounts for accepted launches, and shares its deadline across user-host, unit, manager, and daemon-job cleanup. Partial launches/opens and failed native close calls remain owned. Implemented behavior and failure-injection evidence are recorded in [R1 evidence](R1-EVIDENCE.md). Accepted notification clients now remain owned across failed closes and late accepts, with manager stop retry coverage. Partial watcher/listener opens transfer unfinished cleanup to the manager, with portable and protected-handle regressions. Windows identity/session qualification remains open; stop remains forced Job Object termination until R3.
- [ ] **R1.4 Capture bounds:** stdout/stderr capture emits UTF-8 fragments of at most 64 KiB before newline/EOF, with continuation/partial metadata in journal v3 and the logs API. Queued message data is capped at 4 MiB per invocation and 16 MiB per store, with 12,288 pending fragments per invocation and a 16,384-fragment shared queue cap. Capture keeps draining on overflow; unit status exposes drops and storage errors. Capture/sync waits and journal close have deadlines; at most four sync workers can remain blocked. Tests cover Unicode reconstruction, old journal readers, blocked writes/sync, overflow, failed writes, and recovery after a close timeout. A real child also drains large stdout/stderr and exits while storage is stalled, with observable drops within the invocation budget. Injected disk-full/short-write tests verify loss reporting and recovery after reopening; a missing final newline is restored without rewriting existing bytes or swallowing the next record. A noisy short-line invocation leaves capacity for another unit. Live write failures now retain the exact unwritten record suffix and retry with bounded backoff without reopening; failed close counts abandoned pending records. Actual NTFS volume exhaustion and automatic recovery passed as SYSTEM on a disposable Server Core baseline copy; exact identities are in [R1 evidence](R1-EVIDENCE.md). Queries now use a five-second default deadline and at most four workers; timed-out native operations retain their slots until completion. Injected scan/flush-lock stalls verify bounded admission and recovery. Remaining qualification includes fairness under aggregate overload and total lifecycle admission bounds.

Additional R1 finding: notification tests exposed an early-disconnect transport race and an idle-client shutdown hang. The notify helper now waits for a server-acceptance banner before writing; canceled listeners close accepted clients. The single-send Windows manager test passed 100 race-enabled repetitions, with portable acceptance/cancellation regressions. This alpha transport change requires upgrading the helper and daemon together; it does not acknowledge readiness or replace the invocation/authentication work in R2/R3.

Evidence: local race tests, vet, CI results, focused repetitions, and remaining gaps are recorded in [R1 evidence](R1-EVIDENCE.md). These results do not close the milestone or qualify installation.

Exit gate: real-process tests cover deletion, invalid replacement, recreation while an old invocation lives, large stdout/stderr, no-newline output, exit-before-attach, and failed termination. The previously reproduced failures now pass required regression tests. Race tests and cleanup assertions pass; no owned process becomes unreachable through status/stop and no ambiguous termination is reported as successful.

## R2 — Authoritative lifecycle coordinator

Status: in progress (invocation-definition groundwork; coordinator pending). Dependencies: R1. Outcome: one state owner, concurrent I/O, explicit operation identity.

- [ ] **R2.1 Records/events:** implement immutable configuration revisions, stable runtime records, invocation/operation IDs, generations, typed completion events, and immutable status snapshots.
- [ ] **R2.2 Coordinator:** migrate start/stop/restart, process exit, notify, watchdog, timer/native trigger activation, reload, and user-host lifecycle decisions. Remove direct worker state writes and transaction-result overwrites.
- [ ] **R2.3 Scheduling:** bound admission/workers, preserve completion delivery under overload, coalesce redundant starts, and ensure stop/maintenance precedence. Keep blocking I/O outside the coordinator.
- [ ] **R2.4 Operations:** retain accepted operations across client disconnect; expose queryable outcomes; apply internal deadlines and explicit cancellation; version protocol changes with client compatibility tests.
- [ ] **R2.5 Interleavings:** build deterministic operation-sequence tests with fake clocks and delayed workers; migrate existing tests rather than replacing them with implementation-mirroring tests.

The first R2.1 slice now separates the latest loaded service definition from the
configuration captured before invocation side effects. Status, stop, shutdown,
exit/watchdog cleanup, and automatic recovery retain the captured ownership.
Explicit starts may adopt a new native target only after successful cleanup of
the old one. [Initial R2 evidence](R2-EVIDENCE.md) covers retargeting, service-type
changes, reload during native launch, failed cleanup retries, and automatic
process recovery. Versioned configuration/graph acceptance, revision IDs,
immutable status snapshots, and coordinator migration remain open. Start plans
also capture their member definitions with the graph, retain pending records,
and reject launches/results invalidated by a later stop request. Watches and
timers retain their armed definitions and source identities; queued trigger
starts and late results cannot cross stop/rearm. Triggered service starts share
the restart budget. Transactions default to sixteen concurrent adapter calls
and bounded completion channels. Managers default to 32 admitted start plans;
overloaded watches retain their activation and timers retry with bounded callback
work. Broader admission/resource bounds and the coordinator remain pending.

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
- [ ] **R4.2 User host:** implement the accepted [interactive user admission policy](USER-ADMISSION.md): administrator-enabled users by default, optional delegation through user unit-file presence, and explicit per-SID overrides. Reconcile sessions, file-presence changes, policy changes, and manager exits; acquire fresh tokens on bounded recovery; cancel stale launch/recovery on logoff, admission revocation, disable-linger, and shutdown. File-based admission does not grant headless linger or bypass workload enablement.
- [ ] **R4.3 SCM/maintenance:** report readiness after listener/coordinator initialization; keep diagnostics available when workload configuration is invalid; implement a global maintenance barrier with one deadline across user managers and system units.
- [ ] **R4.4 Security:** verify pipe ownership/DACL/token checks, UAC-filtered users, cross-user rejection, protected privileged paths, reparse-point handling, and no unintended inherited handles.
- [ ] **R4.5 Linger modes:** qualify explicit headless S4U behavior and its credential limitations. Any unqualified optional credential-store mode remains disabled/experimental and is excluded from supported release claims.

Initial R4.2 admission implementation reads protected machine policy, defaults to explicit admission, probes unit-file presence under a duplicated user token with bounded workers, and rejects stale admission results. Existing sessions and policy are reconciled every ten seconds. Portable/native regressions cover revocation, independent linger grants, directory presence, junction rejection, token identity, and deadline ownership. Administrative CLI/installer controls, complete recovery coordination, and real SYSTEM/session evidence remain pending; this does not close R4 or its dependencies.

An [external maintenance helper](../tools/maintenance/README.md) now covers the existing interactive Hermes pilot. Native testing includes injected pre-update failure/recovery, offline updater planning, a real application update with retained backups, disabled legacy-task restoration, listener ancestry, and an unchanged Windows Task Scheduler process. This is operator tooling for the pilot; it does not implement the system daemon's R4.3 maintenance barrier or qualify MSI upgrades.

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
