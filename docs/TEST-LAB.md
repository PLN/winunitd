# Windows qualification lab

September 5, 2026. Implementation plan for R0.4 and the R4–R8 acceptance lanes in [MILESTONES.md](MILESTONES.md). Server Core and Windows 11 Enterprise LTSC evaluation guests have completed fresh unattended installation through the restricted controller. An isolated network, authenticated API client, ownership records, artifact admission, and reboot smoke orchestration are implemented. Automated media preparation, cleanup/reconciliation, and CI integration remain pending.

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

Record OS edition/build/patch level, media and driver hashes, firmware/TPM configuration, guest-agent version, and baseline creation procedure. The initial baseline strategy is a fresh installation for each run, using identified media and versioned bootstrap assets. Keep test accounts distinct from operator identities and generate unique credentials. Remove provisioning secrets after setup. If templates are introduced later, every clone must receive unique machine/network identity and run credentials.

The existing Windows builder was verified as Windows 10 LTSC. It can help build artifacts but cannot supply Windows 11 or Server acceptance evidence. Official Server 2025, Windows 11 Enterprise 25H2, and Enterprise LTSC 2024 evaluation media have been downloaded. Both desktop ISO hashes match Microsoft's published verification PDF. The maintainer selected temporary, restricted internet access for evaluation activation and Windows Update during baseline preparation, disconnected for qualification tests. Implement and verify that boundary before maintaining patched baselines. Only validated targets appear in release support claims.

Media and bootstrap recipes are immutable inputs to a run. Windows updates create a new baseline version rather than changing one during tests. Verify clone/snapshot behavior and performance on the actual storage backend before depending on linked clones. Failed guests may be retained with a bounded expiry and only after evidence collection.

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
- R0.4c: create and validate reproducible Windows baselines — fresh Server Core and Enterprise LTSC evaluation setup and SYSTEM guest-agent handshakes passed. Versioned answer-file/scripts and temporary maintenance policy exist; media rendering/upload remain supervised. Server maintenance converged; LTSC is accepted for development testing with the known issue below.
- R0.4d: implement controller run records, guest handshakes, collection, cleanup and orphan reconciliation — allocation, ownership checks, readiness, artifact admission, reboot smoke, guarded retirement, and read-only inventory audit are implemented. Real retirement and subsequent absence/orphan audit passed.
- R0.4e: connect trusted Gitea dispatch/artifact flow and prove the first reboot smoke — controller reboot smoke passed on Server Core and Enterprise LTSC with downloaded GitHub CI artifacts. Gitea dispatch/download passed, its binaries match GitHub, and that exact artifact passed fresh Enterprise guest smoke.

R0 remains in progress until all of its milestone gates are met. Subsequent supervised bring-up allocated a dedicated pool and one evaluation guest. That guest was moved from its initial network to the isolated bridge after evidence collection; guest-agent access still worked, and it was shut down again. Do not expose it to arbitrary repository jobs or treat it as a maintained template.

## Controller API foundation

`go run ./tools/lab -config PRIVATE_FILE probe` authenticates to the configured pool and optionally verifies HTTP 403 for a known out-of-pool VM. The private JSON configuration has `api_origin`, `ca_file`, `token_file`, `node`, `pool`, and optional `deny_vm_id` fields. The token file contains the Proxmox API authorization value; keep it outside the repository. The probe prints outcomes and a resource count, not private deployment identifiers or response bodies.

The client requires an HTTPS origin, validates the cluster CA and hostname, refuses redirects, bounds requests, and suppresses API response bodies on errors. Local race tests cover trusted/untrusted TLS, token delivery, redirect refusal, response redaction, unsafe origins, reused VM IDs, ownership/network drift, concurrency admission, and artifact identity/tampering.

Additional private configuration fields are `storage`, `bridge`, `mac_prefix`, `first_vm_id`, `last_vm_id`, and an absolute `state_dir`. The dedicated storage and isolated bridge must already exist with scoped API permissions. Commands:

- `create`, with `-vmid`, `-os-iso`, and `-bootstrap-iso`: allocate a fresh guest, persist a random ownership marker before allocation, enforce a two-running-guest limit, and boot setup. Media volume names are limited to dedicated storage. Initial boot key delivery is bounded; that automation still needs a fresh end-to-end repeat after its addition.
- `wait`, with `-vmid`: check pool/node/network and matching ownership record, then await post-setup SYSTEM readiness. It cannot reset an active scenario to ready.
- `smoke`, with `-vmid`, `-artifacts`, and a full `-commit`: reject dirty/wrong-target builds, verify all binary sizes/hashes, transfer bounded chunks, verify guest-side hashes, exercise SCM/unit operations, reboot, check a new invocation and exactly one session-0 process, stop SCM, and collect private evidence.
- `detach`, with `-vmid`: remove only recorded installation media, using the configuration digest, then verify the **current** configuration. Proxmox may defer removal until a power cycle; a pending edit is not a detached ISO. Delete credential-bearing images only after live removal is verified.
- `retire`, with `-vmid`: require completed setup/scenario and detached media, reject extra devices, foreign storage/VM disks, hooks, and retained snapshots, request bounded normal shutdown, recheck ownership, and remove the guest. Verify pool and disk removal before marking it retired. This passed against the completed fresh Enterprise smoke guest; subsequent reconciliation reported its absence with no orphaned disks.

The state directory is private controller storage, not a public artifact directory. CLI operations serialize per guest; provisioning also takes a pool admission lock. A crashed lock requires inspection; do not blindly delete it or treat an unmatched old record as authority over a reused VM ID. Failed scenarios remain available for diagnosis. This is a supervised controller prototype: distributed admission, automatic expiry enforcement, complete failure-log collection, and baseline hash admission are still required before unattended operation.

`go run ./tools/lab -config PRIVATE_FILE reconcile` audits pool members against durable run identities, checks storage for absent recorded guests, and reports expired or unmanaged guests without modifying resources or records. New runs expire after 24 hours; older records use creation time plus 24 hours. Expired guests require operator review and guarded retirement, not deletion based on VM ID alone. The audit passed against the live pool, distinguishing two owned guests from a replaced historical identity. Tests cover expiry, reused IDs, orphaned disks, unmanaged guests, and rejection of incomplete configuration. This audit is a point-in-time observation and does not scan storage for unknown historical VM IDs.

### Maintenance and qualification boundary

Temporary maintenance routing is operated outside the build runner. The first implementation uses a dedicated Linux network namespace, a lab-side veth, a NAT uplink, and namespace-local firewall rules. It permits public HTTP/HTTPS, explicit public DNS, and time synchronization, while denying forwarding to private/reserved networks and unsolicited inbound traffic. A two-hour timer disconnects it. Host-global forwarding/firewall settings remain unchanged. Public HTTPS and denial of the private host management port were verified from Windows. A shortened 15-second timer subsequently exercised automatic disconnection successfully: namespace, ownership marker, and veth disappeared; the bridge retained no host addresses and host forwarding remained disabled. This does not yet constitute complete automated network-boundary regression coverage.

Create `maintenance.lock` in the private controller state directory before enabling this gateway. The smoke command refuses that gate and also rejects guest IPv4/IPv6 default routes. Verify gateway removal and remove maintenance routes before clearing the gate. Shared-runner jobs never receive gateway or Proxmox credentials.

`assets/maintain.ps1` is a supervised SYSTEM preparation script using the Windows Update Agent API. It verifies activation, records license status/expiry, selects nonoptional software updates, excludes feature upgrades, and records per-update results and reboot requirements. The operator reconnects after required reboots and repeats the scan before declaring a patched baseline. Raw logs remain private; no source update or signing credential enters the guest.

Explicitly use UTC for both Windows and the virtual RTC (`localtime=false`). The first fresh runs exposed a two-hour host-local RTC/guest-UTC mismatch. Their lifecycle observations remain recorded, but they are not time-sensitive qualification evidence. Guest Windows Time synchronization was subsequently verified; new recipes use the explicit RTC setting.

### Maintained baseline observations — September 5, 2026

Server 2025 Standard Evaluation Core reached **26100.33296**, with Secure Boot enabled and an active evaluation license. After update installation and reboot, the final Windows Update scan selected zero nonoptional software updates and reported no pending reboot. Per-update results and baseline metadata were collected privately, maintenance routes removed, and the guest powered off.

Enterprise LTSC Evaluation reached **26100.9168** and installed the .NET and Defender follow-ups. Defender antivirus, its service, and real-time protection reported enabled, with platform `4.18.26080.3` and signatures `1.459.59.0`. **Known issue, non-blocking for development testing:** **KB5007651** was repeatedly offered despite per-update success results; no installed/provisioned SecHealthUI package was found. Microsoft describes this as the separate [Windows Security app/service update](https://support.microsoft.com/en-us/servicing/os/windows/2021/10/windows-security-app-update). No update was hidden and no protection disabled to obtain a clean scan. The maintainer accepted this as a known issue and directed that Windows Update troubleshooting stop. Continue winunitd development and qualification work on this baseline; the anomaly does not block project milestones. Retain the observation in test evidence without claiming a clean Windows Update scan.

Each update wave was archived before the next pass. One repeated activation attempt returned a servicing error while license status was already active; preparation now checks that status before requesting activation again. A slow cumulative-update shutdown exceeded the initial hypervisor reboot deadline and eventually left the guest stopped. A normal start resumed servicing; later update reboots used a longer supervised deadline. No forced power-off was used.

### Trusted Gitea artifact experiment

A separate private build-control branch dispatches an explicitly reviewed full source commit, checks out that exact commit with no persisted Git credentials, uses the pinned compiler/build entry point, and uploads only the binaries and manifest. Build-control configuration and deployment endpoints stay in the operator workspace. No automatic GitHub mirroring or release publication is configured.

For source `8da951a54d39dd5f0fe460fe935cde8a5dcc070c`, the downloaded Gitea/Linux cross-build artifact passed its manifest checks and all three binaries matched GitHub's native Windows artifact byte-for-byte. The controller consumed that artifact in a fresh Windows 11 Enterprise Evaluation guest, build **26200.6584**, with Secure Boot enabled and guest agent `110.0.2`. Unattended setup, automatic installation-media boot, SYSTEM handshake, pre-reboot service smoke, enabled-unit recovery with a new invocation after reboot, and SCM stop all passed. The guest had no default route and maintenance routing was disconnected. Media was detached and credential-bearing bootstrap files removed before guarded retirement, which verified guest and disk removal. This is an installation-media baseline, not a patched or release-qualified Enterprise image.

The inherited v3 artifact workflow uploaded successfully but its artifacts were absent from the REST listing. The upstream v4 action rejected the non-GitHub server. The private workflow now pins the [Gitea-recommended compatibility action](https://blog.gitea.com/release-of-1.22.0/) at `ChristopherHX/gitea-upload-artifact@81f940d004763f986ba3582c007fd842dd5cb0d7`; its patch against upstream parent `694cdabd8bdb0f10b2cea11669e1bf5453eed0a6` removes the unsupported-server check. That upload was listed and downloaded through the API. Keep this compatibility dependency under review separately from GitHub's upstream action pin.

## First provisioning observations

### Fresh controller runs — September 5, 2026

Fresh Windows Server 2025 Standard Evaluation Core build **26100.32230** and Windows 11 Enterprise LTSC Evaluation build **26100.1742** passed unattended setup, a SYSTEM guest-agent handshake, and the controller reboot smoke. Secure Boot was confirmed enabled in both guests. Guest-agent file version was `110.0.2`; the serial driver and MSI came from the VirtIO 0.1.285 media. These are media baselines, not current patch-compliance claims.

The smoke consumed the native Windows artifact from GitHub CI run `33951099740`, source `40858a8b593c1aecbc2fbb7452b9ce20c25ed772`:

| Artifact | SHA256 |
| --- | --- |
| winunitd.exe | `617ddbc9a090dd0d0679a4157e5d27c83b940d0d2dfc89b65cfdf70b22dd931e` |
| winctl.exe | `7f5e29234f53c20eed27528d63f3805ac400ce54d5a4109146742c4c7377c0f9` |
| winunit-notify.exe | `f77d3768a29900a69678377b7f8207d49913f918da0065e4a4aeeb8ced43a7c0` |

Both runs verified transferred hashes, unit verification, SCM installation, enable/start, status/logs, stop with no surviving fixture process, and a second start. After reboot, the boot timestamp and invocation changed, exactly one fixture process ran in session 0, and SCM stop removed it. Private controller state, transcripts, and result records were collected. The scripts remain supervised: initial media construction and a boot key were supplied manually, and teardown is not yet automated. This does not qualify a production MSI or the deferred identity/session scenarios.

Verified desktop media:

| Evaluation media | SHA256 |
| --- | --- |
| Windows 11 Enterprise 25H2 en-US x64 | `a61adeab895ef5a4db436e0a7011c92a2ff17bb0357f58b13bbc4062e535e7b9` |
| Windows 11 Enterprise LTSC 2024 en-US x64 | `67cec5865eaa037a72ddc633a717a10a2bed50778862267223ddb9c60ef5da68` |

Both digests match the [Microsoft verification PDF](https://cdn-dynmedia-1.microsoft.com/is/content/microsoftcorp/microsoft/bade/documents/products-and-services/en-us/owned-and-operated/Verify-Download-Win11-Enterprise.pdf) linked from the [Evaluation Center downloads](https://www.microsoft.com/en-us/evalcenter/download-windows-11-enterprise). Enterprise 25H2 was still pending at that checkpoint; its subsequent smoke is recorded in the trusted Gitea artifact experiment above.

### Original supervised bring-up

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

## R1 follow-up qualification

The exact `40ce624` GitHub Windows artifact passed SYSTEM service installation, reboot recovery, SCM stop, and additional output/reload/notify regressions on a disposable copy of the maintained Enterprise LTSC Evaluation baseline. See [R1 qualification evidence](R1-EVIDENCE.md#offline-ltsc-system-qualification) for source and fixture identities, results, and limits. Baseline copying and cleanup preparation were supervised; this does not establish generalized-image or unattended clone provisioning.

The smoke fixture now supplies its own PowerShell execution-policy argument. Failed or truncated guest commands save stdout, stderr, exit/signal status, and command hash in the private controller state directory; console errors do not include guest output or the command body. Both boolean and integer truncation flags are handled, including stderr truncation. This preserves actionable diagnostics without publishing guest data.
