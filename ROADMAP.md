# winunitd roadmap

**Current priority: post-beta stabilization.** Public `0.2.1-beta` shipped on
September 6, 2026; the [B1-B4 delivery gates](docs/BETA-RELEASE.md) are complete.
Prioritize adopter feedback and reproducible defects in the supported core,
preserve documented beta syntax, and continue the R0-R8 architecture and
qualification backlog. [Post-beta tracking](docs/POST-BETA-TRACKING.md) links
GitHub milestones and the next focused work.

Revision 2 - status refreshed September 9, 2026. Original review baseline:
`98abb80`, `0.1.0-alpha`; current published release: `0.2.1-beta`.

This roadmap adopts the direction of the [architecture review](docs/DESIGN-REVIEW.md). It defines future work; it does not mark reviewed defects as fixed. [DESIGN.md](DESIGN.md) specifies the target architecture. [Milestones](docs/MILESTONES.md) define work packages, dependencies, and completion evidence. The [installer plan](docs/MSI-INSTALLER-PLAN.md) specifies package servicing details.

## Product objective

Provide a dependable Windows-native supervisor for cooperating background applications: declarative configuration, process-tree ownership, ordered lifecycle operations, recovery, user execution, and useful diagnostics. The first end-to-end acceptance workload is Hermes on the dev machine.

Keep Go, the portable core and Windows adapters, one SCM system manager, per-user managers, Job Objects, local named pipes, INI units, and the three existing executables. Evolve the current implementation incrementally. Windows retains responsibility for boot, sessions, security, and native service ownership.

## Priorities

1. Preserve ownership and truthful status across output, exit, stop, and configuration changes.
2. Establish one authority for lifecycle decisions and explicit cancellation rules.
3. Make supported systemd-style semantics dependable and Windows differences visible.
4. Qualify real SYSTEM-to-user operation, identity, environment, and manager recovery.
5. Bound logs, triggers, and restart activity; make persistence failures observable.
6. Prove installation, servicing, and the Hermes migration before a public installation release.

The prioritized semantic slice is repeatable `Type=oneshot` execution with
`RemainAfterExit=no` by default, split from R3.3 graceful-stop work. This enables
on-demand maintenance units that return inactive after successful completion.
See [scope and acceptance](docs/POST-BETA-TRACKING.md#priority-repeatable-oneshots).
Existing ownership/cancellation invariants still apply. The default changes
directly, without a versioned migration; `RemainAfterExit=yes` retains active state.

Other feature expansion is paused through the stabilization milestones. Existing triggers, proxies, and resource controls are retained, tested, and corrected; their presence does not establish release qualification. New language implementations, remote control, and generic plugin infrastructure are outside this iteration.

## Milestone sequence

R0, R1, and R2 are **in progress**; later milestones contain groundwork but remain open. Their IDs track the longer-term design, not beta release prerequisites or deadlines. See the milestones for completed work packages and qualification evidence; B1-B4 record the completed first beta delivery. Use the post-beta tracking for current work.

| ID | Outcome | Depends on | Completion signal |
| --- | --- | --- | --- |
| R0 | Reproducible baseline and isolated verification | â€” | Supported compiler pinned; test endpoints isolated; review reproductions retained; Windows qualification harness and packaging spike recorded |
| R1 | Owned workloads remain controllable | R0 | Output deadlock and reload ownership regressions pass; stop failures remain visible; bounded capture |
| R2 | One lifecycle coordinator | R1.1-R1.3 ownership/cleanup invariants | Every lifecycle mutation goes through the coordinator; operation interleavings and stale completions pass invariant tests |
| R3 | Explicit unit and health semantics | R2 | Compatibility matrix, graceful stop, dependency propagation, readiness/liveness, and versioned configuration migration pass conformance tests |
| R4 | Qualified Windows identities and managers | R2, R3 | Real SCM/SYSTEM/user launches, environment, crash recovery, logoff, and bounded maintenance verified |
| R5 | Durable scheduling and operational diagnostics | R2, R3 | Timer crash policy, bounded resource use, storage errors, structured status, and daemon diagnostics verified |
| R6 | Serviceable internal MSI | R3, R4, R5 | Clean install, repair, retained-data uninstall, N-1 upgrade, and injected rollback pass in disposable VMs |
| R7 | Hermes migrated and exercised | R6 | The dev machine uses MSI/SCM ownership; reboot, crash recovery, upgrade, and rollback checks pass; pilot soak recorded |
| R8 | Public release and supply-chain verification | R7 | Public-readiness review complete; signing provider integrated; final signed MSI, attestations, and immutable release verified |

Dependencies describe acceptance, not a ban on overlapping implementation.
R2 closure requires the R1.1-R1.3 ownership/cleanup regressions to remain passing;
it does not wait for R1.4 aggregate fairness, unattended R0 provisioning, or R4
identity/session qualification. Those retain their own release gates. R6's R3-R5
dependencies apply to the fully qualified installer, not the completed narrower
B1-B4 beta MSI contract.

R0 packaging research may proceed alongside runtime stabilization. R4 and R5 may proceed independently once their predecessors close. R8 provider research and public-source preparation may start earlier, but do not waive release gates. No date is promised until the Windows qualification work establishes a reliable estimate.

## Release policy

The published public beta completed B1-B4 in the beta delivery plan. Keep beta
updates labeled as prereleases and publish their limitations. R6-R8 describe the later fully qualified installation
release; they no longer block beta delivery. A beta does not confer a stable
`1.0` API, but documented core unit syntax should remain compatible across betas.

Supported targets must be backed by evidence. The first desktop release targets Windows 11 Enterprise and Enterprise LTSC x64 only. Windows Server 2022/2025 x64, including Core, remain qualification candidates. An untested target is excluded from release claims rather than inferred from compilation. Boot-time linger modes are separately qualified and explicit opt-in. GUI/session attachment, ARM64, and additional account types remain deferred.

The repository is public and the unsigned beta is published. The targeted
history/artifact review and maintainer publication decision are recorded in the
[beta delivery plan](docs/BETA-RELEASE.md#publication-decision). Preserve those
evidence boundaries; public source and beta publication do not close the R8
signed-release gate. Future release inputs still require privacy review. Signing
enrollment and purchases remain separate actions.

Investigate SignPath Foundation first after public-source readiness. Verify its existing-release/reputation requirements and publisher display; acceptance is not guaranteed. Retain Microsoft Artifact Signing for an eligible identity or another trusted signing provider as alternatives. Internal testing does not wait for enrollment. The [signing plan](docs/MSI-INSTALLER-PLAN.md#public-release-and-signing-roadmap) retains provider requirements and references.

Sign payloads before packaging, sign the MSI, then hash and attest the final signed bytes. Publish the completed release immutably. Authenticode, build provenance, and functional qualification serve distinct purposes. Do not promise that signing removes every SmartScreen warning.

## Review-to-delivery traceability

| Review finding or decision | Owning milestone |
| --- | --- |
| Reload loses runtime ownership; oneshot waits before output drain | R1 |
| Stop errors/deadlines are discarded | R1 containment; R2 cancellation; R3 graceful stop; R4 maintenance |
| Competing lifecycle state writers | R2 |
| BindsTo, PartOf, oneshot, PathExists, CPU semantics | R3 |
| User-manager recovery, cross-session handles, profile/environment | R4 |
| Timer overwrite/corruption and ignored persistence failures | R5 |
| Unbounded output lines and slow/full storage | R1 capture bounds; R5 diagnostics and stress qualification |
| Readiness versus health, probe thresholds, restart storms | R3, with R5 operational evidence |
| Supported toolchain, action pinning, fuzzing, VM tests | R0 foundation; each milestone's tests; R8 release verification |
| MSI ownership/rollback and Hermes migration | R6, R7 |
| Public preparation, SignPath, attestations | R8 |
| Mixed proposed and implemented documentation | This revision; R3 behavior reference; each milestone updates status |

## Deferred scope

Do not add full systemd compatibility, Linux runtime support, socket activation, in-process component supervision, remote management, GUI execution/session executors, arbitrary account/credential brokering, automatic updates, or multiple fault-isolated system managers in this iteration. Application-specific health endpoints may describe in-process components, but recovery remains at the owned-process boundary.

Additional features require a concrete workload, an architectural decision, and acceptance tests. Deferred functionality must remain rejected or explicitly experimental in the released parser/API; it must not silently accept configuration that cannot be enforced.

## Tracking and change control

The maintainer owns milestone acceptance. Close a milestone only with implementation commits, automated results, applicable OS/token context, and reproducible acceptance evidence. Store redacted reports with the release or CI artifacts; keep credentials and machine-specific unit files outside the repository. Create implementation issues using IDs such as `R1.1`; issue checkboxes and passing builds alone do not satisfy the exit gate.

Implementation and evidence are recorded in the milestones. [The historical design](docs/archive/DESIGN-v1.md) preserves old source-comment section references; it is not the target specification.
