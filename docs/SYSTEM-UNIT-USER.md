# `User=` on system units

Status: proposed, October 15, 2026, for issue #17. Implementation follows a
native prototype of the launch path described below.

System units run as LocalSystem. Anyone coming from systemd expects `User=` to
work, and a service that defines all its workers in the system manager wants to
choose each worker's account there. Today the only way to run a unit as an
ordinary user is that user's own manager
([running a user's units from SYSTEM](RUNTIME-REFERENCE.md#running-a-users-units-from-system)).

## Contract

- `[Service] User=` names the account the unit runs as: `DOMAIN\name`, a local
  `name`, or a SID. The main process, its descendants and the `ExecStop`
  command all run as that account.
- No password is involved. The system manager obtains the account's token with
  the trusted S4U logon that headless linger already uses, and never with the
  linger record's credential-store fallback. The S4U limits apply: the unit has
  no Windows credentials for other machines, and DPAPI, Credential Manager and
  EFS are not available to it
  ([credential limits](R4-EVIDENCE.md#headless-linger-and-credential-limits)).
- The account's normal profile is loaded for the unit, created on first use,
  and shared with any other process of that account, its own user manager
  included.
- Accepted in `FormatVersion=2` system units of `Type=simple`, `oneshot` and
  `notify`. Rejected in a format 1 file, in user units (a user manager already
  runs as its user), and on `Type=scm` and `Type=scheduled-task` proxies.
  No new format version is needed: the directive was rejected before, so no
  existing file changes meaning.
- `SYSTEM`, `LocalSystem`, `NT AUTHORITY\SYSTEM` and `S-1-5-18` mean the
  default, an ordinary LocalSystem launch. `LocalService` and `NetworkService`
  are rejected for now; they need a service logon rather than S4U and are left
  for later. `Group=` stays rejected.
- `verify` checks the syntax, scope and type only. The account is resolved and
  logged on at every start. An unknown account, a disabled one or an
  unreachable domain controller fails that start like a failed process
  creation, and the unit's `Restart=` policy applies.
- An administrator account gets the token Windows issues for it, which may be
  fully privileged. winunitd does not filter it.
- Only administrators can write system units, so `User=` grants nothing an
  author could not already do by running the unit as LocalSystem.
- Unit status shows the configured account and the SID it resolved to.
  Capabilities list the feature `unit-user-s4u` on the system endpoint.

## Launch

For each invocation, in the system manager:

1. Resolve the account and obtain a primary token by trusted S4U. Check that the
   token's user is the resolved SID.
2. Create a private window station and desktop for that SID with the existing
   desktop helper, as for headless user managers. A process of another account
   cannot use LocalSystem's service desktop.
3. Start a profile keeper: `winunitd.exe` in a waiting mode, launched with
   `CreateProcessWithTokenW` and `LOGON_WITH_PROFILE` on that desktop, in its
   own kill-on-close job that the manager holds. Windows loads or creates the
   profile before the keeper runs and releases it when the keeper ends,
   including when the manager dies and its job handles close.
4. Build the environment from `CreateEnvironmentBlock` for the token, with the
   known-folder variables set as for user managers (`USERPROFILE`, `APPDATA`,
   `LOCALAPPDATA`, `TEMP`, `TMP`, `USERNAME`, `USERDOMAIN`), then the unit's
   `Environment=`, then the manager's own invocation and notify variables.
5. Start the workload with `CreateProcessAsUser`, suspended, with the same
   inherited-handle list (stdin `NUL`, the stdout and stderr pipes), daemon-job
   and unit-job assignment and resume as today, on the private desktop.
6. `ExecStop` runs with the same token, keeper and desktop, so stopping does not
   need another logon.
7. When the workload, its descendants and any stop command have ended, end the
   keeper, release the desktop and close the token. Failed cleanup stays
   visible as pending cleanup, as today.

The notify pipe of such a unit admits the unit's account. Readiness and health
probes run inside the manager and are unchanged. `unit-of-process` is unchanged,
since membership comes from the unit's job.

## Rejected alternatives

- **`CreateProcessWithTokenW` for the workload itself.** It loads the profile,
  but limits the command line to 1024 characters and takes no inherited-handle
  or job list. The environment would also have to be built before the profile
  exists. This stays the fallback if the prototype rules out the keeper.
- **`CreateProcessAsUser` with a manual `LoadUserProfile`.** The manager would own
  the profile reference, which can survive a manager crash and keep the profile
  loaded. The headless user-manager launch removed this for that reason.
- **One cached token per account.** Fewer logons, but group changes would not
  take effect and units of one account would share a logon session. Measure the
  cost of a logon per start before optimizing.
- **Running the unit inside the user's manager.** That needs linger and a second
  manager, and it is not what a system unit means. That route remains available
  separately.

## Verification

- Portable tests: parsing, scope and type rules, the SYSTEM spellings, the
  account reaching the main and stop launch requests, environment precedence and
  the capability.
- Windows tests that run only as LocalSystem, skipped elsewhere like the
  existing S4U test. They cover the token, keeper, desktop, captured output,
  job membership, the process's account and session, the profile variables, and
  the profile's release after the keeper ends. They run as SYSTEM on the
  disposable guest; hosted CI cannot run them.
- A native run on the disposable guest, for a standard local user who has never
  signed in. It checks status, logs, `unit-of-process`, `ExecStop`, a cold boot
  and removal. It also kills the service process while the unit runs, to show
  that the profile is released.

The prototype settles first whether `CreateProcessAsUser` from LocalSystem
accepts an S4U token together with the handle and job lists in session 0, and
how an administrator's S4U token is elevated.
