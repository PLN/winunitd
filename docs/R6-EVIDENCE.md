# Internal installer qualification

## September 13 startup and recovery policy

PR #177 source `dd12c1b9d2436066579dfa4d65a424019fbb718c` passed
[exact-source Windows/Linux CI](https://github.com/PLN/winunitd/actions/runs/34754375316)
and merged with the same tree. Manual and MSI installation now use ordinary
automatic startup, three one-second recovery restarts, an infinite reset period,
recovery for non-crash failures and a 180-second preshutdown timeout.
This fixes [consumer startup feedback #173](https://github.com/PLN/winunitd/issues/173).

The MSI helper captures the previous native policy before servicing, applies the
shared policy after registration and restores the captured policy on rollback.
Each transaction uses a fresh native random token and an exclusive, bounded,
SYSTEM/Administrators-only state file. Missing rollback state is a no-op when
preflight rejected servicing before preparation. Failed restoration retains its
state for diagnosis. Running-service servicing remains rejected.

On the disposable Windows 11 Enterprise LTSC baseline, build 26100, twenty
native SYSTEM repetitions verified policy application, idempotence, restoration
of a deliberately different policy, empty recovery actions and protected state
validation. No selected case was skipped; all temporary service fixtures were
removed.

A clean-source 0.2.3/0.2.4 MSI pair passed eight servicing phases: install,
rejection of running repair, stopped repair, injected upgrade rollback, upgrade,
uninstall, reinstall and final uninstall. The injected failure restored old
payload bytes, stopped service state and the deliberately different delayed-start
and recovery settings. Manual installation afterward selected the same new
startup/recovery policy. Package manifests, payload hashes and complete MSI
logs are retained privately with the source identity.

A real reboot after MSI installation admitted an interactive standard user and
started its enabled workload. Broker control was first observed ready within
20.713 seconds of boot; the admitted user's workload was first observed ready
within 20.993 seconds. These are observation upper bounds on this guest, not
exact transition times or a performance comparison with the consumer's host.
The user workload ran unelevated in session 1. Final cleanup restored the
previous lab broker and user startup settings, removed fixture ownership and
confirmed profile unload. The real application pilot was unchanged.

This qualifies startup/recovery policy and the listed servicing paths. It does
not complete R6. R6.3 data retention is recorded separately. R6.4 platform
acceptance, R6.5 migration tooling, and overall R6 remain open. R7 handoff
and its seven-day soak have not started.

## Maintenance and rollback

The product MSI now embeds the servicing helper described in
[installation](INSTALLATION.md#servicing-a-running-manager) and
[#241](https://github.com/PLN/winunitd/issues/241). Repair, upgrade, and
uninstall quiesce a running manager, then wait up to 180 seconds for SCM stop
and process exit before replacing files. That wait is longer than the
documented 30-second `ServiceControl` wait. `ServiceControl` stays in the
package and runs after the helper. An open payload file or a live service
process aborts replacement. Injected failure restores the captured service
configuration and the previous running or stopped state. Quiet install and
the Application event source `winunitd` are unchanged.

A fresh SYSTEM install of `3ddf5cf` returned 1603 with MSI error 2613 because
custom actions sat between `InstallInitialize` and `RemoveExistingProducts`.
The package now schedules removal after `InstallExecute` and the helper
before `StopServices`. Native SYSTEM servicing evidence is recorded as
`b915fbd-r62-servicing`. Raw MSI logs stay in the private operator
workspace. Do not add guest names, addresses, or private paths to this note.

Native SYSTEM evidence `b915fbd-r62-servicing` covers the following cases
on a disposable machine, as SYSTEM. Skipped cases do not count.

1. Fresh install with no `winunitd` service returns 0. The service is
   running, the Application event source `winunitd` is registered, and the
   transaction state file under Program Files is gone.
2. Repair or upgrade while the manager is running a workload that keeps
   maintenance in progress for more than 30 seconds and less than 180
   seconds. The install waits past 30 seconds, then replaces files, and the
   service is running afterward. The MSI log shows `service-prepare` before
   `InstallFiles` and does not report error 2613. `RemoveExistingProducts`
   is after `InstallExecute`.
3. A workload that does not release within 180 seconds. The install returns
   1603, the log contains `abort replacement`, and the previous payload and
   running service remain.
4. A payload binary held open after the service would otherwise stop. The
   install returns 1603, the log contains `abort replacement` and the file
   name, and that binary's bytes are unchanged.
5. Upgrade and repair with `WINUNITD_TEST_FAIL=1`. Each returns 1603. The
   previous payload hash is restored. Start type, delayed start, recovery
   actions, reset period, non-crash recovery, preshutdown timeout, and the
   previous running or stopped state match the pre-transaction service.
   Repeat once with the service stopped beforehand and confirm it stays
   stopped.
6. Quiet repair without the test property still registers the Application
   event source and does not show installer UI.

Record MSI exit codes, the before/after running state, and payload hashes.
This qualification does not close R6.1, R6.4, R6.5, or overall R6.
The acceptance record and the cases still missing are in
[Acceptance](#acceptance).

## Data and compatibility

R6.3 keeps machine and user units, journals, timer state, and linger
records across repair, upgrade, and uninstall. Those files are not MSI
file components. Mutable directories are permanent and, except for the
data root, do not carry `PermissionEx`, so repair does not overwrite
units, reset enable records, recreate user configuration, or reapply
child ACLs. There is no purge option. The package does not rewrite
on-disk formats. A format change needs an explicit migration; MSI
rollback does not undo it.

Before service stop, replacement, or start, the deferred helper rejects:

- a pre-existing `winunitd` service on a fresh install, including one
  whose paths already match the package
- an unmanaged binary path or a service account other than LocalSystem
- a custom `--base-dir`, which is not adopted or relocated
- a machine scheduled task or machine Run value that launches
  `winunitd.exe` (install, repair, and upgrade; uninstall still removes
  this package)
- a reparse point, unexpected owner, or non-administrator write grant on
  the install directory, `bin`, the data root, or `units`, `enabled`,
  `journal`, `runtime`, `linger`, or `daemon`

The helper does not follow a reparse point and does not try to repair
the target ACL. The MSI log contains `preflight conflict:`. Reinstalling
over retained data starts units that are already enabled. A manual
install that uses another base directory, and the Hermes pilot move,
stay on the explicit migration path (R6.5 / R7).

Automated tests cover the authoring split, the repair/upgrade/uninstall
fixture, and the fail-closed decisions. Native SYSTEM
`preflight-reparse` and `preflight-unmanaged-service` on one guest are
recorded as `9961798-r64-accept` in [Acceptance](#acceptance). Raw logs
stay private. That record is one guest. This note does not close R6.1,
R6.4, R6.5, or overall R6.

## Acceptance

This section is the R6.1 install/repair/uninstall record and the R6.4
platform matrix for [#245](https://github.com/PLN/winunitd/issues/245).
The repeatable harness is `tests/installer/acceptance.ps1`, with the
case list in `tests/installer/acceptance-matrix.json`. It runs one case
on a disposable Windows guest and appends one redacted JSON line to a
private `acceptance-summary.jsonl`. Raw MSI logs stay in that private
directory. The harness does not assign an evidence id.

Acceptance evidence id `9961798-r64-accept` records one SYSTEM guest.
It supersedes draft id `c040800-r64-accept`. The source commit is
`99617988a3cc0bfa93c26a6c2eed0ac76c68520b`. Exact-source CI
[run 35807241580](https://github.com/PLN/winunitd/actions/runs/35807241580)
is green on that commit. The equal-tree MSI sha256 is
`2f57d8388d3a5af79ac87409d950cc326af7bf74c91b0f1d54ca33f6cbf0dd23`.
Raw MSI logs stay private.

The guest reports Windows 10 Enterprise LTSC 2024 Evaluation
(`EnterpriseSEval`), build `26100.9168`, installation type Client.
The identity is SYSTEM and `interactive` is false. This guest is
outside the claimed first-release SKU list below.

Passed cases, each with `status=passed`:

| Case | MSI exit |
| --- | --- |
| `quiet-install` | 0 |
| `system-install` | 0 |
| `repair-fa` | 0 |
| `repair-reinstall` | 0 |
| `locked-file` | 1603 |
| `rollback-test-fail-running` | 1603 |
| `rollback-test-fail-stopped` | 1603 |
| `reinstall-retained` | 0 |
| `uninstall` | 0 |
| `preflight-reparse` | 1603 |
| `preflight-unmanaged-service` | 1603 |

`preflight-reparse` markers include `preflight conflict:` and
`reparse point`. The product log says `is a reparse point; refusing to follow it`.

These cases are `not_run` on this guest and stay missing evidence: `gui-install`, `offline-install`, `non-admin`, `beta-conflict`, `downgrade`, `n1-upgrade`, and `rollback-upgrade`.

R6.1 stays unchecked. Quiet install, both repair paths, and uninstall
passed on this guest. GUI install is still missing on a guest that has
an interactive shell. R6.4 stays unchecked. The claimed SKUs below have
no recorded run. R6.5 and overall R6 stay open. A3 / R4.4 stay deferred.

`b915fbd-r62-servicing` remains the R6.2 servicing record on a
disposable Windows 11 Enterprise LTSC build 26100. It overlaps a fresh
SYSTEM install, quiet repair, a locked payload, and
`WINUNITD_TEST_FAIL` rollback. It does not record GUI install, offline
install, PATH ownership, the Program Files and ProgramData layout, the
absence of a destination Go/WiX/.NET/Python prerequisite, `/fa` and
`REINSTALL=ALL`, uninstall, reinstall over retained units, downgrade,
N-1 upgrade, non-admin rejection, beta UpgradeCode refusal, or the
native preflight lines above. It is not an acceptance evidence id.

Installer version `0.1.0` (ProductCode
`78C43374-5AB7-4E81-B9CF-09E8ACD01133`, UpgradeCode
`A512B91F-1883-40FD-8EDB-5B8C5708DEEA`) is the only recorded product
package. There is no earlier product MSI in this tree. `downgrade`,
`n1-upgrade`, and `rollback-upgrade` stay `not_run` until the operator
supplies an older MSI built from a tip that recorded its own installer
version and ProductCode. The harness checks that the older package
shares this UpgradeCode and has a lower ProductVersion. It does not
invent a version.

The package authors no `ForceReboot` or `ScheduleReboot` action. Every
harness transaction passes `/norestart` and records `reboot_started`.
Exit 3010 is not an expected pass. A locked payload is an abort: exit
1603, log marker `abort replacement`, the prior payload hash unchanged,
and `reboot_started` false.

Claimed SKUs with no recorded run:

- Windows 11 Enterprise x64
- Windows 11 Enterprise LTSC x64
- Windows Server 2022 x64
- Windows Server 2025 x64
- Windows Server Core x64

Server Core is an installation type of the claimed Server 2022 and
Server 2025 SKUs, not a separate release. `gui-install` on Server Core
is `not_applicable` because that installation type has no interactive
GUI. That result does not satisfy `gui-install` for Windows 11
Enterprise, Windows 11 Enterprise LTSC, or a Server installation with a
desktop. A `not_run` or skipped SYSTEM/VM case is missing evidence.
One filled SKU leaves the other claimed SKUs required, so R6.4 stays
unchecked until every applicable case has a passed summary on every
claimed SKU that was actually run, and the unchecked SKUs are listed
here if any remain.

R6.1 stays unchecked until the record also includes a GUI install on a
guest that has an interactive shell, with the layout assertions below.
The SYSTEM quiet, repair, and uninstall rows above do not supply that
GUI run. R6.5 and overall R6 stay open. A3 / R4.4 stay deferred.

### Case catalog

The table is the case contract. `9961798-r64-accept` records the SYSTEM
subset. Continuation id `f1e38a0-r64-continue` is additional and does not
replace that record. A case listed as `not_run` in the SYSTEM record is
still missing evidence.

| Case | Gate | Expected exit | What a pass shows |
| --- | --- | --- | --- |
| `quiet-install` | R6.1, R6.4 | 0 | Clean `/qn` install. Service `winunitd`, display name WinUnit Manager, LocalSystem, automatic start, package binary and `--base-dir`. Application event source `winunitd`. PATH ownership under `HKLM\Software\PLN\winunitd`. Program Files `bin` and `doc`, ProgramData `units`, `enabled`, `journal`, `runtime`, `linger`, `daemon`. Examples stay documentation. No Go, WiX, .NET, or Python prerequisite action |
| `gui-install` | R6.1, R6.4 | 0 | Same assertions with basic installer UI (`/qb!`) on an interactive desktop session. The log contains `UILevel = 3` |
| `system-install` | R6.4 | 0 | Same assertions as SYSTEM |
| `offline-install` | R6.4 | 0 | Same assertions with no default route. The harness removes and restores that route for this case |
| `repair-fa` | R6.1, R6.4 | 0 | Quiet `/fa` repair keeps the layout, event source, and PATH ownership |
| `repair-reinstall` | R6.1, R6.4 | 0 | Quiet `/i REINSTALL=ALL REINSTALLMODE=amus` repair, the same layout assertions |
| `uninstall` | R6.1, R6.4 | 0 | Service, product binaries, event source, and the owned PATH entry are gone. Unrelated PATH entries stay. ProgramData and a retained marker stay |
| `reinstall-retained` | R6.4 | 0 | After uninstall, enabled `alice.service` starts again. Manually started, non-enabled `bob.service` stays inactive. Unit bytes stay |
| `downgrade` | R6.4 | 1603 | Log contains `A newer package is installed.` Payload hash and service state stay |
| `n1-upgrade` | R6.4 | 0 | Older package installs, then `0.1.0` replaces it and the layout assertions pass |
| `locked-file` | R6.4 | 1603 | Log contains `abort replacement`. Payload hash is unchanged. `reboot_started` is false |
| `rollback-test-fail-running` | R6.4 | 1603 | `WINUNITD_TEST_FAIL=1` during `/fa`. Log contains `InjectServiceFailure`. Delayed start and the running state are restored. Same-version bytes match; the delayed-start value is the restore check |
| `rollback-test-fail-stopped` | R6.4 | 1603 | Same injected failure from a stopped service. The service stays stopped and delayed start is restored |
| `rollback-upgrade` | R6.4 | 1603 | Injected failure while moving from the older package to `0.1.0`. The older payload hash, delayed start, and running state are restored |
| `non-admin` | R6.4 | 1602, 1603, or 1625 | Quiet install without an elevated token. An elevated parent uses fixture user alice. The product stays absent |
| `beta-conflict` | R6.4 | 1603 | Unsigned beta is installed. Product install logs `The unsigned beta package is installed.` and leaves the beta in place. The harness discovers a built beta MSI when `-BetaMsi` is omitted, then removes the beta |
| `preflight-reparse` | R6.3 | 1603 | Junction at `%ProgramData%\winunitd`. Log contains `preflight conflict:` and `reparse point`. The link and a marker in the target stay. The product layout stays absent |
| `preflight-unmanaged-service` | R6.3 | 1603 | Pre-existing `winunitd` service whose image is not the package binary. Log contains `preflight conflict:` and `unmanaged winunitd binary path`. The image stays. The product layout stays absent |

### Fields the lab copies

After a real run, copy these fields from the private summary into a
short table under this heading. Assign `evidence_id` then. Use
lowercase letters, digits, and hyphens. Leave the raw log, the evidence
directory, the guest name, account names, addresses, and SIDs in the
private operator workspace.

- `evidence_id`
- `source_commit`
- `installer_version`
- `product_code`
- `upgrade_code`
- `package_sha256`
- `guest_product`
- `guest_edition`
- `guest_build`
- `installation_type`
- `guest_sku`
- `identity`
- `offline`
- `interactive`
- `case_id`
- `status`
- `msi_exit`
- `service_before`
- `service_after`
- `payload_sha256`
- `log_markers`
- `reboot_started`
- `layout_ok`
- `event_source`
- `path_owned`
- `prerequisite_action`
- `data_retained`
- `note`

`status` is `passed`, `failed`, `not_run`, or `not_applicable`. Publish
a case only when `status` is `passed` and `source_commit` is the guest
package's commit. `not_run` stays missing evidence. `not_applicable` is
only the Server Core GUI result described above.

Run from a disposable guest. The evidence directory must sit outside
the repository:

```text
powershell -NoProfile -File tests/installer/acceptance.ps1 -Case list
powershell -NoProfile -File tests/installer/acceptance.ps1 -DisposableGuest -MsiPath <product-msi> -EvidenceDirectory <private-directory> -Case quiet-install
```

Optional switches are `-SourceCommit`, `-EvidenceId`, `-OlderMsi`, and
`-BetaMsi`. A package manifest beside the MSI supplies the commit and
package hash when `-SourceCommit` is omitted. The manifest's `dirty`
flag rejects the run. Fixture unit names are `alice.service` and
`bob.service`. The uninstall retention marker is `carol.marker`.

Suggested order on one elevated guest, each as its own invocation:
`preflight-reparse`, `preflight-unmanaged-service`, `beta-conflict`,
then one clean install (`quiet-install`, `system-install`,
`offline-install`, or `gui-install`), then `repair-fa`,
`repair-reinstall`, `rollback-test-fail-running`,
`rollback-test-fail-stopped`, `locked-file`, `reinstall-retained`, and
`uninstall`. Run `non-admin` from a non-elevated token, or from an
elevated parent which drops to fixture user alice, on a guest where
the product is absent. Repeat the applicable cases for each claimed
SKU. Add `downgrade`, `n1-upgrade`, and `rollback-upgrade` only when an
older recorded package exists.

R6.5 and overall R6 stay open. A3 / R4.4 stay deferred.

## Acceptance continuation

This section is appended after evidence id `9961798-r64-accept`.
That record stays as written. The harness commit
`f1e38a0b80b72c18297f6ba1657473e4fa2dc10b` prepared the remaining cases.
The native continuation on that tip is evidence id
`f1e38a0-r64-continue` in
[f1e38a0-r64-continue](#f1e38a0-r64-continue). Do not reuse
`9961798-r64-accept`. Further runs append another id under this heading.
Use lowercase letters, digits, and hyphens. Copy the summary fields,
including `guest_sku`, into a table here. Leave the raw log, the
evidence directory, the guest name, account names, addresses, and SIDs
in the private operator workspace.

`guest_sku` is one of the claimed names, or `unclaimed`. Evaluation
editions and every other SKU are `unclaimed`. Windows 11 starts at
build 22000, including a guest whose product name still says Windows
10. Server 2022 is build 20348. Server 2025 shares build 26100 with
Windows 11 24H2, so a server row also requires a Server product name
or installation type. An `unclaimed` run does not fill a claimed row
and does not check R6.1 or R6.4.

### Harness for the remaining cases

`gui-install` starts `msiexec /i` with `/qb!` (basic UI, no cancel
button) and requires an interactive desktop: the process is user
interactive and its session id is greater than 0. Server Core stays
`not_applicable`. A pass still requires the layout, service identity,
Application event source, PATH ownership, and no destination Go, WiX,
.NET, or Python action. The verbose log must contain `UILevel = 3`.
Run this case in the desktop session. A session 0 or remoting session
is `not_run`.

`offline-install` removes IPv4 and IPv6 default routes for that case
only, requires the product MSI and the evidence directory to be on a
local disk, and restores the routes afterward. The summary records
`offline` true. Gateway addresses stay out of the summary. A restore
failure fails the case.

`non-admin` runs quiet install without an elevated token. When the
parent is already a standard user or a filtered administrator token,
msiexec runs as that token. That token must be able to read the product
MSI and write the evidence directory. When the parent is Administrator or
SYSTEM, the harness creates local user alice, starts msiexec as alice,
then removes alice. If alice already exists, the case fails and does
not reuse that account. The summary identity for the fixture path is
`standard-user`. Fixture names stay alice, bob, and carol.

`beta-conflict` uses `-BetaMsi` when it is passed. Otherwise it
discovers `winunitd-<version>-x64-beta.msi` whose UpgradeCode is
`9443AE50-251B-4A46-9465-B835A3A27133`. The search is `dist/beta/<version>/`,
the top level of `packaging/beta`, and the directory beside the product
MSI. The version is read from the package. The harness does not select
a version that is not in one of those files, and it does not search
`packaging/beta/obj`.

`downgrade`, `n1-upgrade`, and `rollback-upgrade` use `-OlderMsi` when
it is passed. Otherwise they look for `winunitd-<version>-x64.msi`
under `dist/wix/<version>/`, the top level of `packaging/wix`, or
beside the product MSI. A file counts only when its UpgradeCode is
`A512B91F-1883-40FD-8EDB-5B8C5708DEEA` and its ProductVersion is lower
than `0.1.0`. No such package is in this tree. `packaging/wix/build.ps1`
refuses to build any installer version other than `0.1.0`. The unsigned
beta packages `0.2.0` and `0.2.1` use UpgradeCode
`9443AE50-251B-4A46-9465-B835A3A27133`. The packaging-spike fixtures use
UpgradeCode `6BF75153-09DE-4D44-B053-74B01921B136`. None of those is a
product N-1. Those three cases stay `not_run`. Do not invent a
ProductVersion.

### Still not_run

The harness commit left these cases without a continuation result:
`gui-install`, `offline-install`, `non-admin`, `beta-conflict`,
`downgrade`, `n1-upgrade`, and `rollback-upgrade`. Evidence id
`f1e38a0-r64-continue` records the later guest. `non-admin` and
`beta-conflict` are `failed` there. `offline-install`, `gui-install`,
`downgrade`, `n1-upgrade`, and `rollback-upgrade` are `not_run`.
`failed` and `not_run` stay missing evidence.

Claimed SKUs with no recorded run:

- Windows 11 Enterprise x64
- Windows 11 Enterprise LTSC x64
- Windows Server 2022 x64
- Windows Server 2025 x64
- Windows Server Core x64

The recorded guest for `9961798-r64-accept` is Windows 10 Enterprise
LTSC 2024 Evaluation (`EnterpriseSEval`), build `26100.9168`. That
identity is `unclaimed`. It does not satisfy a claimed row.
`gui-install` on Server Core remains `not_applicable` and does not
satisfy GUI install for a desktop SKU.

R6.1 stays unchecked until a GUI install on an interactive claimed
desktop SKU (Windows 11 Enterprise or Windows 11 Enterprise LTSC x64)
is copied under this heading. R6.4 stays unchecked. One claimed SKU
would still leave the others required, and the three older-package
cases stay open until a real older product MSI exists. R6.5 and
overall R6 stay open. A3 / R4.4 stay deferred.

Suggested continuation order on one claimed Windows 11 Enterprise or
Windows 11 Enterprise LTSC desktop, each as its own invocation:
`beta-conflict`, `gui-install` from the interactive desktop,
`offline-install`, then `non-admin`. Add `downgrade`, `n1-upgrade`,
and `rollback-upgrade` only when an older recorded product MSI is
supplied. Repeat the applicable cases for each claimed Server guest
the lab can provide. Server Core skips `gui-install`.

### f1e38a0-r64-continue

Evidence id `f1e38a0-r64-continue` records one agent-driven continuation.
The source tip is `f1e38a0b80b72c18297f6ba1657473e4fa2dc10b`. Exact-source
CI [run 35810305710](https://github.com/PLN/winunitd/actions/runs/35810305710)
is green on that tip. This note is a later commit. That green run does
not cover this commit, and this commit has no green CI run yet.

The equal-tree product MSI for the tip is installer `0.1.0`. The package
manifest commit matched the tip. Raw logs stay in the private operator
workspace.

The guest is Windows 11 Enterprise LTSC Evaluation (Eval), installation
type Client, build `26100.9168`. Agent-driven jobs ran as SYSTEM.
`interactive` is false, and the guest had no console session. `guest_sku`
is `unclaimed`. This Evaluation guest does not fill the claimed Windows
11 Enterprise or Windows 11 Enterprise LTSC rows.

The SYSTEM subset under `9961798-r64-accept` stays as written. Those
cases were not re-run.

| Case | Status | Note |
| --- | --- | --- |
| `offline-install` | `not_run` | Default route indeterminate |
| `non-admin` | `failed` | Could not start msiexec as fixture user alice |
| `beta-conflict` | `failed` | Product refused with the unsigned-beta-installed marker (MSI exit 1603). Beta cleanup failed and left the service running |
| `gui-install` | `not_run` | No interactive session |
| `downgrade`, `n1-upgrade`, `rollback-upgrade` | `not_run` | No older product MSI with UpgradeCode `A512B91F-1883-40FD-8EDB-5B8C5708DEEA` and ProductVersion lower than `0.1.0`. No ProductVersion is invented |

`failed` and `not_run` are missing evidence. `beta-conflict` showed the
refusal marker and exit 1603, and the case is `failed` because beta
cleanup left the service running.

Claimed SKUs with no recorded run:

- Windows 11 Enterprise x64
- Windows 11 Enterprise LTSC x64 (non-Eval)
- Windows Server 2022 x64
- Windows Server 2025 x64
- Windows Server Core x64

Server Core remains an installation type of the claimed Server 2022 and
Server 2025 SKUs. This Evaluation guest fills none of those rows. R6.1
and R6.4 stay unchecked. R6.5 and overall R6 stay open. A3 / R4.4 stay
deferred. [#245](https://github.com/PLN/winunitd/issues/245) stays open.

