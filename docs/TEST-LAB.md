# Windows qualification lab

September 5, 2026. Implementation plan for R0.4 and the R4–R8 acceptance lanes in [MILESTONES.md](MILESTONES.md). Infrastructure discovery established an available three-node Proxmox cluster, shared image storage, working administrative access, and an existing Gitea instance with online Windows/Linux build runners. No registered VM templates were found. Provisioning and CI integration remain pending.

Private inventory, endpoints, VM IDs, key paths, and credentials belong in the operator's infrastructure workspace. They must not enter project workflows, committed test results, or guest images. This document is intentionally portable to another installation.

## Execution responsibilities

| Layer | Responsibility |
| --- | --- |
| Repository scripts | Compiler/build/test/packaging entry points, fixture setup, assertions, result schema |
| GitHub CI | Existing portable/Windows checks; eventual protected release and provenance workflow |
| Private Gitea CI | Queue reviewed integration work, consume immutable artifact identity, retain qualification evidence |
| External lab controller | Allocate fresh guests, enforce limits, survive guest reboot, collect results, remove owned resources |
| Disposable Windows guest | Exercise the actual SCM, SYSTEM, user session, filesystem, and MSI behavior |
| Real pilot | Application authentication, practical operation, and long-duration acceptance after VM qualification |

Reuse the existing local CI pattern of build -> package -> test and artifact transfer. Shared builders compile/package; acceptance guests are clean and disposable. Workflow YAML stays thin around repository scripts. Existing local automation can operate the private controller without becoming a runtime prerequisite for winunitd.

Keep the current GitHub source remote during initial lab bring-up. A private Gitea test repository or explicit trusted dispatch can run the same commit. Never qualify a floating branch name alone. If development later moves to Gitea with GitHub as a public mirror, define a separate, auditable source/artifact publication step; discovery did not establish that the reference project's GitHub publication is performed by its inspected CI workflow.

## Baselines and capacity

Create a maintained Windows desktop baseline and a Server Core baseline from identified installation media. Start with two concurrent guests at a provisional 4 vCPU, 8 GiB RAM, and 80 GiB thin disk each; tune after measuring tests and storage behavior. Set controller concurrency and disk-retention quotas so failed guests cannot accumulate indefinitely.

Record OS edition/build/patch level, media and driver hashes, firmware/TPM configuration, guest-agent version, and baseline creation procedure. Keep test accounts distinct from operator identities. Remove provisioning secrets and runner registrations before sealing the baseline. A cloned guest gets unique machine/network identity and run credentials. Do not clone a personalized workstation or shared build server as a clean acceptance image.

The existing Windows builder was verified as Windows 10 LTSC. It can help build artifacts but cannot supply Windows 11 or Server acceptance evidence. Available media includes desktop Windows and Server 2022; Server 2025 media and current target patch levels require preparation. Only validated targets appear in release support claims.

Templates are immutable inputs to a run. Windows updates create a new baseline version rather than changing one during tests. Verify clone/snapshot behavior and performance on the actual storage backend before depending on linked clones. Failed guests may be retained with a bounded expiry and only after evidence collection.

## Controller and run contract

Each run has a unique ID, source commit, artifact hashes, baseline version, test scenario, guest identity, and expiry. Keep controller state outside the guest so a reboot or failed installation cannot lose the record needed for cleanup.

1. Admit a reviewed commit/artifact and scenario; verify capacity and allowlisted baseline.
2. Allocate a guest within the dedicated pool/network and annotate it with controller ownership and run ID.
3. Boot and wait for a bounded management handshake. Transfer the exact artifact and verify its hash in the guest.
4. Run fixture setup and tests, establishing the required identity/session explicitly.
5. Reboot or inject failures as the scenario requires; reconnect with bounded waits and continue from durable controller state.
6. Collect structured results, sanitized event/daemon/MSI logs, exit codes, and relevant process/job observations.
7. Tear down only resources whose pool, identity, ownership tag, and run record all match. Retain failures only under an explicit expiry; use a separate reconciler for abandoned runs.

Keep provisioning authority outside arbitrary repository build steps. The controller uses a restricted credential for its pool and required operations; guests never receive cluster credentials, source-write tokens, or signing authority. Use a dedicated test network with permitted controller/artifact access. Unreviewed public PRs stay on unprivileged CI until admitted to a suitable isolated lane. Ephemeral runner registration alone is not guest cleanup or network isolation.

## Acceptance scenarios

| Lane | Proof | Milestone |
| --- | --- | --- |
| Baseline smoke | Fresh guest, SYSTEM fixture, reboot, result collection, bounded cleanup | R0.4 |
| Process correctness | Large output, no newline, removed/invalid unit, stale launch, failed termination | R1–R3 |
| Identity/session | SCM session 0, standard-user logon, cross-session launch, environment, logoff/recovery | R4 |
| Persistence/stress | Full disk, interrupted write, timer replay, burst triggers, slow output consumer | R5 |
| Servicing | Exact MSI install/repair/uninstall, N-1 upgrade, injected rollback, retained data | R6 |
| Signed acceptance | Downloaded final signed MSI and embedded payload verification plus servicing smoke | R8 |

Guest-agent/remote SYSTEM execution does not prove an interactive desktop session. Session scenarios explicitly establish logon, verify SID/session/token context, and exercise logoff. Physical sleep/resume and the real application soak remain separate evidence; VM restart is not a substitute for either.

The first end-to-end smoke uses an isolated fixture and current binaries because no MSI exists yet. Once R6 packaging is ready, extend the same controller path to install -> start workload -> reboot -> verify -> uninstall/rollback. Do not mark runtime or MSI acceptance passed because the controller smoke passed.

## Artifact and release identity

Results must bind to a full source commit and artifact hash. Local CI qualifies exact downloaded/uploaded artifacts rather than independently rebuilding an assumed equivalent release. For R8, sign payloads, package/sign the MSI, then hash and attest final bytes; the lab verifies those final bytes. An unavailable or failed required lab result blocks release acceptance.

Store detailed private logs locally with retention limits. Publish only sanitized qualification summaries identifying platform, artifact, scenario, and outcome. Returning a status to GitHub is a deliberate CI integration, not an assumption that Gitea results automatically become GitHub checks. Provider-compatible signing provenance remains a separate gate.

## Bring-up work packages

- R0.4a: inventory/access/media discovery — performed; private evidence recorded separately.
- R0.4b: allocate isolated pool/network and restricted controller identity — pending.
- R0.4c: create and validate reproducible Windows baselines — pending.
- R0.4d: implement controller run records, guest handshakes, collection, cleanup and orphan reconciliation — pending.
- R0.4e: connect trusted Gitea dispatch/artifact flow and prove the first reboot smoke — pending.

R0 remains planned until all of its milestone gates are met. No infrastructure resources were created during discovery.
