# winunitd 0.3.0

The first public release of the product MSI, WinUnit Manager, for Windows 11
Enterprise and Enterprise LTSC x64. It replaces the unsigned 0.2.1-beta package.
No Go or .NET runtime is needed on the destination.

The MSI is not code-signed yet, so Windows SmartScreen may warn when you open
it. Check the download instead:

```powershell
# Compare with SHA256SUMS from this release.
Get-FileHash .\winunitd-0.3.0-x64.msi -Algorithm SHA256
# Confirm that this repository's release workflow built it from the v0.3.0 tag.
gh attestation verify .\winunitd-0.3.0-x64.msi --repo PLN/winunitd
```

## Install

From an elevated terminal:

```powershell
msiexec /i .\winunitd-0.3.0-x64.msi /qb
```

The package installs the LocalSystem `winunitd` service, puts `winunitd.exe`,
`winctl.exe` and `winunit-notify.exe` in `%ProgramFiles%\winunitd\bin` and adds
that folder to PATH (`REMOVE=AddToPath` opts out). Examples and documentation
are in `%ProgramFiles%\winunitd\doc`. No workload is enabled. See the
[installation guide](INSTALLATION.md) for repair, upgrade and removal.

**From 0.2.1-beta:** the MSI refuses to install while the beta is present.
Back up `%ProgramData%\winunitd`, stop the service, uninstall the beta, then
install 0.3.0. Units, enablement and logs carry over; enabled units start again
([details](INSTALLATION.md#moving-from-the-unsigned-beta)).

## New since 0.2.1-beta

- **Installer and servicing.** Repair and in-place upgrade stop the service
  cleanly and roll back if the new version fails to start. Data under
  `%ProgramData%\winunitd` is kept across repair, upgrade and removal. The
  installer refuses conflicting services and unsafe directories, and an
  optional compatibility floor keeps unattended servicing from running an
  unsuitable build. The service writes a daemon log and Windows Application
  log events.
- **Units.** A cooperative stop command (`ExecStop`), repeatable oneshots,
  fixed or exponential restart backoff, loopback readiness probes and
  probe-based health checks, `BindsTo`, and an explicit unit format 2
  (`FormatVersion=2`) whose policies are listed in the
  [unit reference](UNIT-REFERENCE.md). Timers handle calendar, boot and
  interval triggers across clock changes and DST. Journals stay within bounded
  disk use.
- **Control.** Start, stop and restart return operation IDs for
  `winctl operation`, and `winctl cancel` stops unfinished starts and
  restarts; a maintenance mode quiesces the manager for servicing;
  `winctl capabilities` reports the running build; status stays available
  under heavy load.
- **User managers.** Each admitted user gets a manager in their own session
  for their units under `%LOCALAPPDATA%\winunitd`. `winctl enable-linger`
  keeps a user's manager running without a sign-in. Both work for local and
  Active Directory domain accounts, including roaming profiles.

## Limits

- Tested on Windows 11 Enterprise LTSC build 26100. Other editions, Windows
  Server and ARM64 are not supported yet; see [SUPPORT.md](../SUPPORT.md).
- Microsoft Entra ID (cloud) accounts are not supported.
- A lingering user's services run without the user's credentials: network
  shares need credentials the service supplies, and DPAPI, Credential Manager
  and EFS are unavailable. While linger holds a roaming profile, the profile
  stops roaming on that machine
  ([headless user managers](OPERATIONS.md#headless-user-managers)).
- Experimental: the `headless-store-uri` linger mode,
  `RequiresInteractiveSession`, and persistent timers (`Persistent=`).
- Upgrades from the 0.1.x pilot builds were exercised. Downgrades are refused.

## Qualification

Each area was tested on disposable Windows guests and in a pilot that ran a
real workload under the installed service, including a 24-hour soak. The
[milestones](MILESTONES.md) and the evidence documents they link record what
was tested and what was not.
