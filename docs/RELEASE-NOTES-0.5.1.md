# winunitd 0.5.1

This patch fixes repeated installation and lifecycle behavior. For Windows
11 Enterprise and Enterprise LTSC x64; no Go or .NET runtime is needed on the
destination.

The MSI is not code-signed yet, so Windows SmartScreen may warn when you open
it. Check the download against this release's `SHA256SUMS` and build provenance:

```powershell
Get-FileHash .\winunitd-0.5.1-x64.msi -Algorithm SHA256
gh attestation verify .\winunitd-0.5.1-x64.msi --repo PLN/winunitd `
  --signer-workflow PLN/winunitd/.github/workflows/release.yml `
  --source-ref refs/tags/v0.5.1
```

## Install or upgrade

From an elevated terminal:

```powershell
msiexec /i .\winunitd-0.5.1-x64.msi /qb
```

Over 0.5.0 this upgrades in place: the service stops, the new version starts,
and units, enablement, logs and linger grants carry over. See the
[installation guide](https://github.com/PLN/winunitd/blob/v0.5.1/docs/INSTALLATION.md)
for installation and earlier package upgrades.

## Fixes since 0.5.0

- Repeating installation of the exact installed MSI restores a service that
  was running before the command. A previously stopped service remains stopped
  on a plain repeat install.
- When a process exits and its replacement fails to start, Start and Status
  report failure and the launch error instead of stale active/running status
  with no main process.
- Daemon verification rejects `User=` in user-manager units, matching reload
  and file verification.
- Daemon shutdown no longer reports a cleanup failure if a child exits between
  the liveness check and termination. Failed termination of a still-running
  child remains visible.

## Limits

Support and limitations remain as documented for
[0.5.0](https://github.com/PLN/winunitd/blob/v0.5.1/docs/RELEASE-NOTES-0.5.0.md#limits).
Signing remains deferred.
