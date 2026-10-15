# winunitd 0.4.0

For programs that supervise their own workers through winunitd: a query that
tells which unit a process belongs to, a public Go client, and a supported way
for SYSTEM or an administrator to run units as an ordinary user without a
sign-in. For Windows 11 Enterprise and Enterprise LTSC x64; no Go or .NET
runtime is needed on the destination.

The MSI is not code-signed yet, so Windows SmartScreen may warn when you open
it. Check the download instead:

```powershell
# Compare with SHA256SUMS from this release.
Get-FileHash .\winunitd-0.4.0-x64.msi -Algorithm SHA256
# Confirm that this repository's release workflow built it from the v0.4.0 tag.
gh attestation verify .\winunitd-0.4.0-x64.msi --repo PLN/winunitd `
  --signer-workflow PLN/winunitd/.github/workflows/release.yml `
  --source-ref refs/tags/v0.4.0
```

## Install or upgrade

From an elevated terminal:

```powershell
msiexec /i .\winunitd-0.4.0-x64.msi /qb
```

Over 0.3.0 this upgrades in place: the service stops, the new version starts,
and units, enablement, logs and linger grants carry over. A new install is as
described for [0.3.0](https://github.com/PLN/winunitd/blob/v0.4.0/docs/RELEASE-NOTES-0.3.0.md#install) and in the
[installation guide](https://github.com/PLN/winunitd/blob/v0.4.0/docs/INSTALLATION.md).

## New since 0.3.0

- **Which unit a process belongs to.** `winctl unit-of-process PID` and the
  `unit-of-process` control method name the unit whose job holds a process:
  its main process, any descendant, or its stop command. The reply carries the
  invocation, whether the process is the main one, and its creation time, so
  a program that serves a named pipe can attribute a client and rule out a
  reused process ID. Each manager answers for its own units
  ([runtime reference](https://github.com/PLN/winunitd/blob/v0.4.0/docs/RUNTIME-REFERENCE.md#which-unit-a-process-belongs-to)).
- **Go client.** `github.com/PLN/winunitd/client` dials the system manager or
  any user's manager, checks the identity of the process serving the pipe,
  and offers typed calls with context cancellation. Its types are the control
  protocol's. Until 1.0 its API may change between minor releases.
- **Running a user's units from SYSTEM.** `winctl --user=<user or SID>` talks
  to that user's manager, which administrators and LocalSystem may control.
  Status reports each manager's unit directory. The
  [procedure](https://github.com/PLN/winunitd/blob/v0.4.0/docs/RUNTIME-REFERENCE.md#running-a-users-units-from-system) covers
  enable-linger, placing the unit file, reload, start and removal.
- **Documentation.** The runtime reference now says how a lingering user's
  manager takes over from the interactive one at sign-out, restarting enabled
  units once, and what a headless manager's units cannot reach. Statements that
  headless linger was not yet qualified are corrected.

## Limits

Unchanged from [0.3.0](https://github.com/PLN/winunitd/blob/v0.4.0/docs/RELEASE-NOTES-0.3.0.md#limits): Windows 11 Enterprise
LTSC build 26100 is the tested platform; Microsoft Entra ID accounts are not
supported; a lingering user's services have no Windows credentials for other
machines and no DPAPI, Credential Manager or EFS; `headless-store-uri`,
`RequiresInteractiveSession` and `Persistent=` are experimental; downgrades are
refused. System units still run as LocalSystem; `User=` is planned.

## Qualification

Hosted CI ran the unit, race and integration tests for each change. On a
disposable guest, an MSI with these changes ran the whole SYSTEM-to-user
procedure for a user who never signed in, including a cold boot, the Go client
and removal ([evidence](https://github.com/PLN/winunitd/blob/v0.4.0/docs/R4-EVIDENCE.md#units-placed-by-system-in-a-lingering-users-manager)).
Before publication, this MSI was installed clean and as an upgrade from 0.3.0 on
a disposable guest.
