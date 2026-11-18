# winunitd 0.6.0

A `User=` system unit can now start with its account's signed-in session
instead of an S4U logon, so a unit of a domain account keeps working away from
the domain network while that account is signed in. For Windows 11 Enterprise
and Enterprise LTSC x64; no Go or .NET runtime is needed on the destination.

The MSI is not code-signed yet, so Windows SmartScreen may warn when you open
it. Check the download against this release's `SHA256SUMS` and build provenance:

```powershell
Get-FileHash .\winunitd-0.6.0-x64.msi -Algorithm SHA256
gh attestation verify .\winunitd-0.6.0-x64.msi --repo PLN/winunitd `
  --signer-workflow PLN/winunitd/.github/workflows/release.yml `
  --source-ref refs/tags/v0.6.0
```

## Install or upgrade

From an elevated terminal:

```powershell
msiexec /i .\winunitd-0.6.0-x64.msi /qb
```

Over 0.5.1, 0.5.0, 0.4.0 or 0.3.0 this upgrades in place: the service stops,
the new version starts, and units, enablement, logs and linger grants carry
over. See the
[installation guide](https://github.com/PLN/winunitd/blob/v0.6.0/docs/INSTALLATION.md)
for installation and earlier package upgrades.

## New since 0.5.1

- **`UserLogon=` on `User=` system units.** A `User=` unit is logged on by S4U
  at every start, and S4U of a domain account needs a domain controller. On a
  machine away from its domain network, with the account signed in from
  cached credentials, such a unit could not start, restart or be upgraded.
  `[Service] UserLogon=session-or-s4u` beside `User=` makes each start use a
  copy of the token of a session the account is signed in to, active or
  disconnected, and S4U only when there is none or its token cannot be used:

  ```ini
  [Service]
  ExecStart=C:\Apps\worker.exe
  User=S-1-5-21-1-2-3-1001
  UserLogon=session-or-s4u
  Restart=always
  RestartSec=2s
  ```

  Sessions are matched by the SID in their token, never by name, and another
  user's session is never used. The process still runs in session 0, on the
  private desktop and in its unit job, with nothing visible in the user's
  session; an administrator under UAC gets the full linked token, never the
  filtered one.

  A session logon has what S4U lacks: Windows credentials for other machines,
  DPAPI, Credential Manager and EFS, and the `INTERACTIVE` group in place of
  `NETWORK` or `BATCH`. Whether a start gets one or the other depends on the moment it
  starts, so a unit must not depend on the difference.

  When the session whose logon an invocation uses signs out, the manager stops
  that invocation (with `ExecStop`) and records
  `session <N>, whose logon this invocation used, signed out`. Windows does not
  end these processes itself. The stop counts as a failed exit, so
  `Restart=on-failure` and `always` start the unit again, by S4U or another
  session. `ExecStop` of such an invocation runs on the invocation's own logon,
  also at sign-out and off the domain network. Lock, unlock, disconnect,
  reconnect and other users' sessions do nothing to the unit. Status shows the logon in use as `Logon: s4u` or
  `Logon: session 2` (`userLogon` and `userSession` in the JSON), the journal
  records each start's logon, and capabilities list `unit-user-session` on the
  system endpoint.

  The default is unchanged: `s4u`, the same as leaving the directive out, so
  existing units behave as before. `verify` and reload reject `UserLogon=`
  without a `User=` naming an account other than SYSTEM, and in format 1 files,
  user units and proxies
  ([session logon](https://github.com/PLN/winunitd/blob/v0.6.0/docs/UNIT-REFERENCE.md#session-logon),
  [design](https://github.com/PLN/winunitd/blob/v0.6.0/docs/SYSTEM-UNIT-USER.md#session-logon)).

## Fixes since 0.5.1

- A workload that exits with code 259, the value Windows uses for a process
  still running, is now seen as exited with status 259. The manager could
  previously wait on it indefinitely and block cleanup. User-manager liveness
  checks also use the process handle's state.
- A notify connection is accepted only once the unit's main process is
  assigned, and only from that process or a process in its job; anything else
  is refused.
- Each `WantedBy=` entry must be a single name. Path separators, drive or
  stream colons, control characters and other characters invalid in Windows
  file names are rejected by `verify` and reload, and `enable` checks every
  name before it writes any link.
- Workload journal files get a protected Windows ACL that allows only the
  manager's account, SYSTEM and Administrators. Rotated archives keep it, and
  existing files are repaired when the unit's journal opens; journals of units
  that never open again keep their previous ACL until retention removes them,
  and grants added to journal files by hand are replaced.
  [SECURITY.md](https://github.com/PLN/winunitd/blob/v0.6.0/SECURITY.md) now
  describes the lifecycle metadata visible to readers of the shared Windows
  Application event log.
- Built with Go 1.27.4, which fixes
  [GO-2026-6809](https://pkg.go.dev/vuln/GO-2026-6809) (`crypto/x509`) and
  [GO-2026-6859](https://pkg.go.dev/vuln/GO-2026-6859) (`net/http`) in the
  Go 1.27.2 that built 0.5.1.

## Limits

- `UserLogon=`: an invocation keeps the logon it started with, so a unit
  started by S4U is not moved to a session when its account signs in; its next
  start uses the session. A `User=` written as `DOMAIN\name` is still resolved
  before every start, which may ask a domain controller; write the SID to avoid
  that. A sign-out is missed only if its notification is dropped and Windows
  reuses the session ID within one poll interval; the invocation then keeps its
  logon until its next restart, as user managers do. At machine shutdown,
  users sign out before the service stops, so such a unit may get a sign-out
  stop and one S4U start before the ordered stop.
- Otherwise support and limitations remain as documented for
  [0.5.0](https://github.com/PLN/winunitd/blob/v0.6.0/docs/RELEASE-NOTES-0.5.0.md#limits).
  Signing remains deferred.

## Qualification

Hosted CI ran the unit, race and integration tests on Windows and Linux. For
`UserLogon=` they cover the directive's rules, the capability, session order
and SID matching over a fake native layer, the full-token rule, the fallback
and its errors, status, the stop at sign-out with `Restart=`, a sign-out during
a start, and the logoff hook. The tests that need LocalSystem and a signed-in
test account do not run in hosted CI.
<!-- native-run -->
