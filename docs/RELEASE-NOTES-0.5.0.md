# winunitd 0.5.0

System units can now run as an ordinary account: `User=` names the account,
and winunitd logs it on without a password. For Windows 11 Enterprise and
Enterprise LTSC x64; no Go or .NET runtime is needed on the destination.

The MSI is not code-signed yet, so Windows SmartScreen may warn when you open
it. Check the download instead:

```powershell
# Compare with SHA256SUMS from this release.
Get-FileHash .\winunitd-0.5.0-x64.msi -Algorithm SHA256
# Confirm that this repository's release workflow built it from the v0.5.0 tag.
gh attestation verify .\winunitd-0.5.0-x64.msi --repo PLN/winunitd `
  --signer-workflow PLN/winunitd/.github/workflows/release.yml `
  --source-ref refs/tags/v0.5.0
```

## Install or upgrade

From an elevated terminal:

```powershell
msiexec /i .\winunitd-0.5.0-x64.msi /qb
```

Over 0.4.0 or 0.3.0 this upgrades in place: the service stops, the new version
starts, and units, enablement, logs and linger grants carry over. A new install
is as described for [0.3.0](https://github.com/PLN/winunitd/blob/v0.5.0/docs/RELEASE-NOTES-0.3.0.md#install)
and in the [installation guide](https://github.com/PLN/winunitd/blob/v0.5.0/docs/INSTALLATION.md).

## New since 0.4.0

- **`User=` on system units.** In a `FormatVersion=2` system unit of
  `Type=simple`, `oneshot` or `notify`, `User=` runs the main process, its
  descendants and `ExecStop` as that account: `DOMAIN\name`, a local name or a
  SID. No password is involved: before each start the system manager logs the
  account on by S4U, the logon headless linger uses. It loads the account's
  normal profile, creating it on first use, and gives the process the
  account's environment. A start that cannot log the account on fails, and
  `Restart=` applies. Status shows the account and its SID, and capabilities
  list `unit-user-s4u`
  ([accounts](https://github.com/PLN/winunitd/blob/v0.5.0/docs/UNIT-REFERENCE.md#accounts),
  [design](https://github.com/PLN/winunitd/blob/v0.5.0/docs/SYSTEM-UNIT-USER.md)).

## Limits

- A `User=` unit has no Windows credentials for other machines, and DPAPI,
  Credential Manager and EFS are not available to it. Its logon is a network
  logon: the processes are not in `INTERACTIVE`, `SERVICE` or `BATCH`, and
  `taskkill.exe` refuses to run; `Stop-Process` works. An administrator account
  runs with its full, elevated token. `LocalService`, `NetworkService` and
  `Group=` are not supported.
- Otherwise unchanged from [0.3.0](https://github.com/PLN/winunitd/blob/v0.5.0/docs/RELEASE-NOTES-0.3.0.md#limits):
  Windows 11 Enterprise LTSC build 26100 is the tested platform; Microsoft
  Entra ID accounts are not supported; `headless-store-uri`,
  `RequiresInteractiveSession` and `Persistent=` are experimental; downgrades
  are refused.

## Qualification

Hosted CI ran the unit, race and integration tests. Tests that need
LocalSystem and a real account ran on a disposable guest. An MSI with these
changes also ran a `User=` unit there for an account that never signed in,
covering its stop command, a cold boot, a killed service process, accounts that
cannot log on, and removal
([evidence](https://github.com/PLN/winunitd/blob/v0.5.0/docs/R4-EVIDENCE.md#system-units-with-user)).
Before publication, this MSI was installed clean, ran a `User=` unit, and was
installed as an upgrade from 0.4.0 on a disposable guest.
