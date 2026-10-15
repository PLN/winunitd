# `User=` on system units

Status: implemented for issue #17, October 15, 2026. The contract for unit
authors is in the [unit reference](UNIT-REFERENCE.md#accounts); this note
records the design and why.

System units ran only as LocalSystem. Anyone coming from systemd expects
`User=` to work, and a service that defines all its workers in the system
manager wants to choose each worker's account there. The other way to run a
unit as an ordinary user is that user's own manager
([running a user's units from SYSTEM](RUNTIME-REFERENCE.md#running-a-users-units-from-system)).

## Contract

- `[Service] User=` names the account: `DOMAIN\name`, a local `name`, or a SID.
  The main process, its descendants and `ExecStop` run as that account.
- No password is involved. The token comes from the trusted S4U logon that
  headless linger uses, never from the linger record's credential-store
  fallback. The S4U limits apply: no Windows credentials for other machines,
  and no DPAPI, Credential Manager or EFS
  ([credential limits](R4-EVIDENCE.md#headless-linger-and-credential-limits)).
- The account's normal profile is loaded, created on first use, and shared
  with every other process of that account.
- Accepted in `FormatVersion=2` system units of `Type=simple`, `oneshot` and
  `notify`; rejected in format 1 files, in user units and on proxies. These
  rules apply to the directive's presence. Then the SYSTEM spellings (`SYSTEM`,
  `LocalSystem`, `NT AUTHORITY\SYSTEM`, `S-1-5-18`, any case) select the
  ordinary LocalSystem launch. `LocalService` and `NetworkService`, by name or
  SID (`S-1-5-19`, `S-1-5-20`), are rejected for now; they need a service logon.
  No new format version is needed: the directive was rejected before, so no
  existing file changes meaning.
- `verify` checks syntax, scope and type. The manager resolves the account
  before each start. A failed resolution or logon fails the start with an
  account error, and `Restart=` applies. Nothing falls back to LocalSystem.
- An administrator account runs with the elevated token Windows issues for an
  S4U logon; the lab run confirmed that. winunitd does not filter it.
- On an installed manager only administrators can write the system unit
  directory, so `User=` lets no one run anything they could not already run
  as LocalSystem. `User=` does not itself enforce that protection.
- Status reports `user`, from the definition that owns the invocation, and
  `userSid`, the SID that invocation resolved, together. Journal entries of
  system units keep an empty user SID. Capabilities list `unit-user-s4u` on
  the system endpoint.

## Launch

Each process a `User=` unit starts, the workload or its `ExecStop` command,
has its own account session, opened by the launcher and released with the
process:

1. Log the account on by trusted S4U and check that the token's user is the
   resolved SID.
2. Start a profile keeper, `winunitd.exe --profile-keeper`, with that token,
   through the headless path the user host already uses. It runs suspended
   until it is placed in its own kill-on-close job.
   `CreateProcessWithTokenW` with `LOGON_WITH_PROFILE` loads the profile, or
   creates it, before it returns, and Windows holds the profile while the
   keeper runs. The keeper runs on a private window station and desktop that
   admit only SYSTEM and the account.
3. Build the environment from `CreateEnvironmentBlock` for the token, now that
   the profile is loaded. Set the known-folder variables as for user managers
   and remove `HOMEDRIVE` and `HOMEPATH`. Apply the unit's `Environment=`,
   then the manager's own invocation and notify variables. The manager's own
   environment is not used.
4. Start the process with `CreateProcessAsUser`, the same token and the private
   desktop, suspended, with the inherited-handle list (stdin `NUL`, the stdout
   and stderr pipes), the daemon job, the unit job and the resume of every
   other launch. There is no command-line limit beyond Windows' own.
5. If the keeper ends while the process runs, the launcher terminates the
   process's job. The main process's exit then reports a failure, and
   `Restart=` applies.
6. When the process's cleanup completes, end the keeper and close the token.
   That releases this process's profile reference; the profile unloads once no
   other process of the account holds it.

`ExecStop` logs on afresh, as the account in the definition that owns the
invocation, so a reload that changed `User=` still stops the running invocation
as the account it started with. Holding one logon for the whole invocation
would avoid the second logon, but a oneshot with `RemainAfterExit=yes` would
then keep a logon and the profile while no process runs. If the stop command
cannot log on, stopping proceeds as when `ExecStop` fails.

The notify pipe of such a unit admits the resolved account. The launcher
reports the main process and its job before the process runs, so the pipe
accepts only that process and its job from the start. Readiness and health
probes run inside the manager and are unchanged. `unit-of-process` attributes
the processes through the unit job as before.

## Rejected alternatives

- **`CreateProcessWithTokenW` for the workload itself.** It loads the profile,
  but limits the command line to 1024 characters, takes no inherited-handle or
  job list, and needs the environment before the profile exists. Using it
  would need a different launch design.
- **`CreateProcessAsUser` with a manual `LoadUserProfile`.** The manager would
  own the profile reference, which can survive a manager crash and keep the
  profile loaded. The headless user-manager launch removed this for that
  reason.
- **One cached token per account.** Fewer logons, but group changes would not
  take effect, and units of one account would share a logon session.
- **Running the unit inside the user's manager.** That needs linger and a
  second manager, and it is not what a system unit means; that route remains
  separately available.

## Verification

- Portable tests: parsing, scope, type and the special spellings; the resolved
  account reaching the workload's launch and the invocation's account reaching
  `ExecStop` after a reload; the environment without the manager's own; status;
  a failed resolution followed by a successful recovery start; the capability.
- Windows tests that run only as LocalSystem with a test account and skip
  elsewhere. One process writes and reads HKCU and a profile file and must
  run as the account in session 0 inside its unit job. Ending the keeper must
  end the workload. Two processes of one account must keep the profile until
  the last one ends.
- A native run on the disposable acceptance guest with the MSI, for a standard
  account that never signed in: status, logs, `unit-of-process`, `ExecStop`,
  a cold boot, a killed service process, an account that cannot log on, and
  removal.
