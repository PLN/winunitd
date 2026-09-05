# Windows qualification lab

September 5, 2026. Implementation plan for R0.4 and the R4–R8 acceptance lanes in [MILESTONES.md](MILESTONES.md). A first Windows Server 2025 Core evaluation guest has passed supervised smoke and packaging-fixture tests. An isolated network and restricted API account are provisioned; the controller's authenticated probe is implemented. Maintained templates, run orchestration, and CI integration remain pending.

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

Create maintained Windows 11 Enterprise, Enterprise LTSC, and Server Core baselines from identified installation media. The maintainer selected Enterprise/LTSC for the initial desktop support matrix. Start with two concurrent guests at a provisional 4 vCPU, 8 GiB RAM, and 80 GiB thin disk each; tune after measuring tests and storage behavior. Set controller concurrency and disk-retention quotas so failed guests cannot accumulate indefinitely.

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
- R0.4b: allocate isolated pool/network and restricted controller identity — pool and bridge allocated, with no physical uplink or host IP/IPv6 address. A dedicated API account has pool-scoped VM permissions and required storage/network permissions; TLS-authenticated pool access and denial of an out-of-pool VM configuration were verified. Run orchestration must retain these boundaries.
- R0.4c: create and validate reproducible Windows baselines — first Server Core evaluation guest deployed; reproducible maintained baselines pending.
- R0.4d: implement controller run records, guest handshakes, collection, cleanup and orphan reconciliation — pending.
- R0.4e: connect trusted Gitea dispatch/artifact flow and prove the first reboot smoke — supervised local reboot smoke passed; Gitea integration pending.

R0 remains in progress until all of its milestone gates are met. Subsequent supervised bring-up allocated a dedicated pool and one evaluation guest. That guest was moved from its initial network to the isolated bridge after evidence collection; guest-agent access still worked, and it was shut down again. Do not expose it to arbitrary repository jobs or treat it as a maintained template.

## Controller API foundation

`go run ./tools/lab -config PRIVATE_FILE probe` authenticates to the configured pool and optionally verifies HTTP 403 for a known out-of-pool VM. The private JSON configuration has `api_origin`, `ca_file`, `token_file`, `node`, `pool`, and optional `deny_vm_id` fields. The token file contains the Proxmox API authorization value; keep it outside the repository. The probe prints outcomes and a resource count, not private deployment identifiers or response bodies.

The client requires an HTTPS origin, validates the cluster CA and hostname, refuses redirects, bounds requests, and suppresses API response bodies on errors. Local race tests cover trusted/untrusted TLS, token delivery, redirect refusal, response redaction, and unsafe origins. Provisioning, immutable artifact admission, durable run records, cleanup, and orphan reconciliation are not implemented by this probe.

## First provisioning observations

The initial guest uses official Windows Server 2025 Standard Evaluation media, WIM index 1 (Server Core), build 26100.32230. It has four vCPUs, 8 GiB RAM, an 80 GiB sparse SATA disk, UEFI with enrolled Microsoft keys, and an emulated Intel NIC. These conservative device choices let Windows Setup run without injecting storage/network drivers. Future baselines should explicitly qualify their chosen VirtIO devices.

An unattended answer file partitioned a newly allocated empty disk with `WillWipeDisk=false`. A separate temporary ISO carried the answer file, binaries, VirtIO serial driver, and guest-agent MSI. Follow the site's VMID/MAC/DHCP convention before first boot and verify the resulting address inside Windows. Never infer an available VMID from a single node's guest list; check the cluster.

The VirtIO serial driver installed during the specialize pass, but the guest-agent MSI from VirtIO 0.1.285 failed with MSI 1603 / error 1722 in its VSS `RegisterCom` action. The same MSI installed successfully after Windows Setup finished. Schedule guest-agent installation after setup in the reproducible builder; fail and retain diagnostics if installation fails. The first bring-up used authenticated, encrypted WinRM to diagnose and complete this step, then verified guest-agent execution as SYSTEM.

Evaluation activation succeeded without a subscription key. Record each guest's actual expiry and rebuild or retire it before expiry; snapshots do not extend evaluation rights. Detach installation media and remove generated credential-bearing answer files and bootstrap ISOs after setup. Keep the administrator credential only in the private operator secret store.

## First smoke evidence — September 5, 2026

Source: `0bcc83b5cfcb6690a66e3318969cdd299f0e0125`, built locally with Go 1.27.0. Guest-side SHA256 checks matched the transferred artifacts:

| Artifact | SHA256 |
| --- | --- |
| winunitd.exe | `c5db278f8c2bfe4bb222dc914096992dc7a09cab0f53ea83ee2ecb974a62de7e` |
| winctl.exe | `a93756f75a85733d38f1a28560ac58271b6f9e9df1ea0038cd99db16e2f75feb` |
| Official evaluation ISO | `7b052573ba7894c9924e3e87ba732ccd354d18cb75a883efa9b900ea125bfd51` |

The ISO digest identifies the downloaded bytes; it was not compared against a separately published Microsoft checksum. Media was downloaded from the [Microsoft Evaluation Center](https://www.microsoft.com/en-us/evalcenter/download-windows-server-2025) through its official HTTPS redirect.

Passed on Server Core build 26100.32230:

- Guest-agent command execution verified as `NT AUTHORITY\SYSTEM`.
- Current CLI service installation registered winunitd as LocalSystem with automatic delayed startup.
- Fixture verification, enable/start, active status, journal output, stop, absence of the stopped process, and second start.
- Normal guest reboot changed the boot timestamp; delayed SCM startup launched the enabled fixture with a new invocation ID and exactly one process in session 0. Observed first post-reboot output was about 132 seconds after boot.
- Journal output was available from both boots.
- Explicit SCM stop removed the fixture process. Logs and unit files were collected outside the guest before shutdown.

The guest is retained powered off, with host autostart disabled, for follow-up work. It is not a generalized template. Private scripts, complete logs, activation expiry, and resource ownership are recorded in the operator workspace. This supervised smoke does not establish repeatable CI provisioning, current patch compliance, standard-user/session behavior, runtime fault recovery, MSI servicing, or release qualification.
