# Revision 2 implementation milestones

**Delivery status:** [B1-B4 public beta gates](BETA-RELEASE.md) completed with
`0.2.1-beta` on September 6, 2026. Prioritize supported-path defects and adopter
feedback while continuing the R0-R8 design and qualification work. Complete
coordinator migration, exhaustive qualification and the seven-day soak remain
separate acceptance requirements; beta publication does not close them.

September 9, 2026. Implements [ROADMAP.md](../ROADMAP.md) and [Design v2](../DESIGN.md).
R0, R1 and the initial R2 ownership work are **in progress**; later milestones
remain planned with some groundwork delivered. [Post-beta tracking](POST-BETA-TRACKING.md)
maps the GitHub milestones and focused issues to these gates. Documentation and
issue creation do not complete implementation or substitute for acceptance.

## Acceptance rules

For each completed milestone, record implementation commits, automated results, applicable Windows build and execution identity, manual acceptance procedure/results, and remaining limitations. A skipped SYSTEM/VM test is missing evidence. Redact host-specific data. The maintainer accepts closure against the exit gate; individual checked tasks do not override it.

Do not change existing pilot ownership merely to run tests. Use isolated endpoints, process jobs, data directories, and disposable VMs. Runtime fixes include permanent regressions. Documentation-only changes require link/consistency checks rather than a runtime test rerun.

## R0 — Reproducible baseline and qualification harness

Status: in progress. Dependencies: none. Outcome: a safe, repeatable way to prove subsequent work. Initial implementation and qualification evidence: [R0 baseline](R0-BASELINE.md).

- [x] **R0.1 Toolchain:** implemented in `7b788e9` and `40858a8`; local checks and all CI lanes pass, and native Windows/Linux cross-build artifact hashes match. Compiler/action pins, artifact metadata, and vulnerability/update policy are recorded in [build policy and evidence](BUILDING.md).
- [x] **R0.2 Isolation:** prevent integration tests from connecting to the real system/user manager; give fixtures dedicated endpoint/data namespaces and bounded process cleanup. Implemented in `15ccd9d`; test-daemon endpoint guards and isolated user-manager scenarios pass.
- [x] **R0.3 Review reproductions:** retain opt-in failing reproductions for reload ownership and large-output oneshots with small-output controls. Recorded in `15ccd9d`; deletion/invalid replacement and large stdout/stderr reproduce the defects, small-output controls pass, independent cleanup completes. R1 converts them into passing required regressions.
- [ ] **R0.4 Windows harness:** Remaining to close: automate supervised media/baseline preparation and qualify standard-user/session and failure-injection lanes with repeatable cleanup. [Delivered lab evidence](TEST-LAB.md).
- [x] **R0.5 Packaging spike:** implemented in `ea7fb35`; clean-commit SYSTEM qualification passed install, repair, running/stopped rollback after old-product removal, stop-deadline failure, slow upgrade, and uninstall. Tooling terms, native MSI limitations, helper strategy, exact artifacts, and remaining production requirements are recorded in the [packaging spike](PACKAGING-SPIKE.md).

Exit gate: supported builds and existing isolated test lanes pass; the two review defects are reproducible without touching Hermes; VM provisioning/smoke and packaging-spike evidence can be repeated. Runtime defects remain open for R1. This milestone does not qualify the application for installation.

## R1 — Ownership, output, and failure containment

Status: in progress alongside remaining supervised R0 qualification. Dependencies for closure: R0. Outcome: urgent correctness fixes in the existing implementation before structural refactoring.

- [ ] **R1.1 Reload:** Remaining to close: consolidate the required real-process/native ownership matrix and obtain maintainer acceptance of retained status/log/stop and suppressed restart after removal. [Evidence](R1-EVIDENCE.md), [revision/trigger coverage](R2-EVIDENCE.md).
- [ ] **R1.2 Activation:** Remaining to close: finish the post-creation failure/exit-before-attach qualification matrix jointly with R1.3; retain passing large-output/stop-during-start regressions. Completed-oneshot beta semantics remain unchanged until R3. [Evidence](R1-EVIDENCE.md).
- [ ] **R1.3 Stop results:** Remaining to close: finish the exact-identity termination/handle-failure matrix and confirm every failed cleanup remains owned, observable and retryable. SYSTEM-to-user session qualification belongs to R4; graceful stop belongs to R3. [Evidence](R1-EVIDENCE.md).
- [x] **R1.4 Capture bounds:** queue fairness plus combined memory/worker, loss, quiet progress and status/stop measurements qualified at `ff822d2` by exact-source Windows/Linux race CI and isolated native SYSTEM/standard-user pressure runs. Three cycles per identity verify storage recovery and cleanup stability. Overall lifecycle admission remains R2.3; historical journal file/retention bounds remain R5.3. [Measured evidence and limits](R1-EVIDENCE.md#combined-manager-pressure-measurements), [issue #95](https://github.com/PLN/winunitd/issues/95).

Implementation history and exact qualification identities are in [R1 evidence](R1-EVIDENCE.md).

Exit gate: real-process tests cover deletion, invalid replacement, recreation while an old invocation lives, large stdout/stderr, no-newline output, exit-before-attach, and failed termination. The previously reproduced failures now pass required regression tests. Race tests and cleanup assertions pass; no owned process becomes unreachable through status/stop and no ambiguous termination is reported as successful.

## R2 — Authoritative lifecycle coordinator

Status: in progress (handler migration; admission/snapshots pending). Closure depends on preserved R1.1-R1.3 ownership/cleanup invariants, not completion of unrelated R1.4 or R4 qualification. Outcome: one state owner, concurrent I/O, explicit operation identity.

- [ ] **R2.1 Records/events:** a bounded immutable manager-local unit/active-operation snapshot is exposed through `winctl snapshot`; native and user-host observations remain separate domains. Complete coordinator-wide identity-bearing event coverage. Accepted/invocation/armed revisions and operation IDs are implemented. [Evidence](R2-EVIDENCE.md), [snapshot contract](OPERATIONS.md).
- [ ] **R2.2 Coordinator:** Remaining to close: finish the [writer audit](LIFECYCLE-WRITERS.md), including timer/session policy and admission decisions, using the mutex-serialized handler contract in Design section 2. Preserve independent blocking I/O and prohibit transaction-result replay. [Evidence](R2-EVIDENCE.md).
- [ ] **R2.3 Scheduling:** Remaining to close: bound every command/worker class and prove completion progress under saturated admission, with stop/maintenance precedence. Handler extraction alone does not satisfy this gate. [Admission groundwork](R2-EVIDENCE.md).
- [ ] **R2.4 Operations:** Remaining to close: reconcile coordinator-wide lifetime coverage. Explicit start/restart cancellation preserves successfully completed members; accepted stops continue cleanup. Accepted operation contexts, deadlines, disconnection behavior and bounded history are delivered; history is intentionally nonpersistent. [Contract](OPERATIONS.md), [evidence](R2-EVIDENCE.md#accepted-operation-lifetimes).
- [ ] **R2.5 Interleavings:** Remaining to close: complete the invariant/operation-sequence matrix through public paths and delayed adapters; replace overlapping handler-only tests rather than duplicating them. Track the [race-suite budget](TEST-HARDENING.md).

Implementation history, operation/admission limits and exact qualification identities are in [R2 evidence](R2-EVIDENCE.md) and [OPERATIONS.md](OPERATIONS.md).

Exit gate: each invariant in Design v2 §3 has a test/evidence mapping. Start/stop/restart/reload/exit/timeout/trigger permutations, late successful launches, stale probes, overload, client disconnect, and shutdown during launch preserve ownership and ordering. A source audit finds no second lifecycle authority. Independent units still perform I/O concurrently.

## R3 — Unit semantics, compatibility, and application health

Status: planned. Dependencies: R2. Outcome: an explicit v2 behavior contract and controlled migration from alpha semantics.

- [ ] **R3.1 Reference/migration:** publish `docs/UNIT-REFERENCE.md` with format marker, exact directive names/defaults, accepted/rejected features, argv grammar, and a compatibility table. Add a dry-run converter for legacy PathExists and CPU policy; never silently rewrite unit files. The accepted oneshot default changes directly to `RemainAfterExit=no`, with `yes` available explicitly; no oneshot migration layer is required.
- [ ] **R3.2 Dependencies:** implement/test BindsTo on unexpected disappearance and PartOf stop/restart participation, including reverse members absent from the root's Wants. Document Requires versus ordering and partial transaction results.
- [x] **R3.3a Repeatable oneshots:** completed/inactive oneshots by default and explicit RemainAfterExit merged in PR #116 after exact-head Windows/Linux CI at `c1a87e3`. Delivered separately from graceful stop; [scope and evidence](POST-BETA-TRACKING.md#priority-repeatable-oneshots) cover repeat invocation, overlap, ordering, failure and cleanup.
- [x] **R3.3b Services/stop:** single ExecStop with captured context, separate bounded helper ownership/output, shared graceful/forced budget, late-launch retention and cleanup retries. Exact-source CI and eight genuine SYSTEM/headless-user cases passed at `b8f356e`, including hung helper/descendant termination and both oneshot modes. [Evidence and limits](R3-EVIDENCE.md#tracked-cooperative-stop). Console/GUI signals remain deferred.
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

R4.1 launch groundwork now disables handle inheritance, obtains the default
environment from the target token without broker variables, and resolves target
AppData known folders. Native stream/environment and retained-cleanup regressions
pass in the test account's session. The broker uses Windows-owned interactive
profiles with a retained registry handle; headless loading still requires
abrupt-death lifetime qualification. Managed profiles remain limited to local
machine accounts. The `68d3397` disposable guest candidate passed genuine
SYSTEM-to-standard-user launch, user-token control, workload identity/environment,
manager and broker crash recovery. Its manually loaded interactive profile leaked
after broker death; ordinary logoff after a fresh boot passed. The `8a61393`
registry-handle replacement passed the full repeated launch, user-manager crash,
broker crash and logoff sequence, including profile release and no resurrection.
This is not R4.1 acceptance; the remaining session/security/linger matrix stays open.

SCM startup now awaits an explicit listener/coordinator readiness signal before
reporting Running. Control starts before boot workload activation, and an initial
configuration rejection leaves control available for repair. Native host tests
cover delayed readiness, startup failure, progress checkpoints, stale interrogate
snapshots and stop before a late readiness callback. Genuine SCM boot/repair
qualification remains pending; this does not implement the maintenance barrier.

The next R2/R4 session slice repairs missed logoffs through authoritative session
enumeration, rejects stale snapshots, preserves replacement sessions and linger,
and retries retained idle cleanup. Public UserHost regressions cover those
interleavings and fresh-token recovery after an interactive manager exits.
Bounded recovery workers/backoff and real SYSTEM/session qualification remain open.

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

- [ ] **R8.1 Public readiness:** public source and unsigned beta publication are complete under the recorded [B4 review and decision](BETA-RELEASE.md#publication-decision). For the signed release, review source history and release inputs for credentials, personal names, real machine names, internal addresses, and host artifacts; review historical content against the recorded publication decision and resolve newly discovered exposure before publishing new artifacts. Complete behavior, installation, recovery, support, license, and security-reporting documentation. Record qualified platforms/modes. Source visibility changes remain a separate action.
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

Full pilot qualification precedes the R8 signed-release acceptance gate; the narrower B1-B4 beta has already shipped. Package prototypes can still be built earlier without being declared installation-ready.
