# winunitd 0.6.1

This patch fixes `ExecStop` during manager shutdown and adds a preflight
check for upgrades from affected releases. For Windows 11 Enterprise and
Enterprise LTSC x64; no Go or .NET runtime is needed on the destination.

The MSI is not code-signed yet, so Windows SmartScreen may warn when you open
it. Check the download against this release's `SHA256SUMS` and build provenance:

```powershell
Get-FileHash .\winunitd-0.6.1-x64.msi -Algorithm SHA256
gh attestation verify .\winunitd-0.6.1-x64.msi --repo PLN/winunitd `
  --signer-workflow PLN/winunitd/.github/workflows/release.yml `
  --source-ref refs/tags/v0.6.1
```

## Install or upgrade

From an elevated terminal:

```powershell
msiexec /i .\winunitd-0.6.1-x64.msi /qb
```

Over 0.6.0, 0.5.1, 0.5.0, 0.4.0 or 0.3.0 this upgrades in place: the service
stops, the new version starts, and units, enablement, logs and linger grants
carry over. The installed manager performs that stop. The incoming MSI checks
for affected system units before it starts maintenance. A `preflight conflict`
leaves the current manager and workloads running. For a planned upgrade, drain
units that need `ExecStop=` first as the
[installation guide](https://github.com/PLN/winunitd/blob/v0.6.1/docs/INSTALLATION.md#upgrading-from-060-or-earlier)
describes.

## Fixes since 0.6.0

- Shutdown runs `ExecStop` for active invocations. The maintenance quiesce
  that servicing uses, and the service stop, seal the manager against new
  starts and then stop every unit. Releases 0.3.0 to 0.6.0 also
  refused the `ExecStop` helper of that stop pass. An active unit with
  `ExecStop=` then lost its processes without `ExecStop`, the quiesce did not
  report `quiesced`, and the MSI failed with 1603 and rolled back. Rollback
  restarts the previous manager, so the version stayed the same and the
  running units had been terminated. After the seal, only the shutdown pass
  can launch a fresh stop helper.

## Limits

- Managers up to 0.6.0 keep this defect until they are replaced, also for the
  upgrade to 0.6.1, a repair or uninstall of those versions, and a service
  stop or machine shutdown. Units without `ExecStop=` and inactive units are
  not affected.
- Otherwise support and limitations remain as documented for
  [0.6.0](https://github.com/PLN/winunitd/blob/v0.6.1/docs/RELEASE-NOTES-0.6.0.md#limits).
  Signing remains deferred.

## Qualification

The manager regression test shuts down with an active `ExecStop=` unit,
checks that its helper ran, and verifies that the workload and helper both
exited. Separate checks cover external helper requests after the seal and
late helper requests during final cleanup.
