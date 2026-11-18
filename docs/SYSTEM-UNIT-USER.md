# `User=` on system units

Status: implemented for issue #17, October 15, 2026. The contract for unit
authors is in the [unit reference](UNIT-REFERENCE.md#accounts); this note
records the design and why. `UserLogon=` (issue #39) adds the
[session logon](#session-logon).

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
- `[Service] UserLogon=s4u|session-or-s4u`, added later (see
  [session logon](#session-logon)), chooses per start where the token comes
  from. `s4u` and an absent directive are the contract above. It is valid only
  beside a `User=` that passed these rules and names an account other than
  SYSTEM. Capabilities list `unit-user-session` on the system endpoint.

## Launch

Each process a `User=` unit starts, the workload or its `ExecStop` command,
has its own account session, opened by the launcher and released with the
process:

1. Log the account on by trusted S4U, or with `UserLogon=session-or-s4u` take
   a signed-in session's token first ([session logon](#session-logon)), and
   check that the token's user is the resolved SID.
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
cannot log on, stopping proceeds as when `ExecStop` fails. An S4U invocation
stops by S4U even if its account has signed in since. An invocation on a
session logon is the exception: its `ExecStop` does not log on, see
[Session logon](#session-logon).

The notify pipe of such a unit admits the resolved account. The launcher
reports the main process and its job before the process runs, so the pipe
accepts only that process and its job from the start. Readiness and health
probes run inside the manager and are unchanged. `unit-of-process` attributes
the processes through the unit job as before.

## Session logon

A domain account's S4U logon needs a domain controller, so a `User=` unit of a
laptop's own domain account cannot start, restart or be upgraded away from the
network even while that account is signed in from cached credentials.
`UserLogon=session-or-s4u` starts it with the signed-in session's token in
that case.

Selection, at every start and by SID only: enumerate the active and
disconnected sessions, never session 0; query each one's token with
`WTSQueryUserToken` and read the user SID from the token, never the session's
user name, which may need the domain. Another user's token is closed at once.
The first match among the active sessions by ascending ID, then the
disconnected ones, is used. A query that fails for another reason is reported
when no session matches, because whose session it was cannot be known.

Preparation: a UAC-filtered administrator token is replaced by its linked full
token, so the unit has the rights an S4U start gives; failure to obtain it is a
failure of the session path, and the filtered token is never used. The token
is duplicated as a primary token and the copy's session set to 0. From there
the launch is steps 2 to 6 above, unchanged: the keeper adds a reference to the
profile the sign-in loaded, and the process runs on the private desktop in the
daemon and unit jobs. No token is cached; each workload opens its own, and
only its `ExecStop` uses a copy of it (below).

Fallback: when no session matches, or any step of the session path fails up
to the account session being open (token, linked token, duplicate, session,
keeper, profile, environment, account lookup), the same start logs on by S4U.
When both fail, the error names both, as
`User=<SID>: no signed-in session of the account; S4U logon for <SID>: ...`
or with the session path's own error first. Two cases are not followed by S4U:
a session path that could not release what it opened, which keeps it for a
cleanup retry as an S4U failure does, and a session that signed out while the
launch was under way, which fails the start
(`session <id>: signed out during the launch`). A launch that fails after the
account session opened is an account error, to which `Restart=` applies, when
its session has signed out; otherwise it stays a plain launch error, as for
S4U.

`ExecStop`: the stop command of an invocation on a session logon runs on a
copy of that invocation's own token, duplicated from the token its account
session holds, with a profile keeper, desktop and environment of its own that
end with the stop command. It selects no session and logs on nowhere: the token
stays valid after its session signed out and needs no domain controller, so
`ExecStop` runs at sign-out and off the network too. If the copy or its account
session cannot be opened, stopping proceeds as when `ExecStop` fails; there is
no fallback to S4U. The invocation's account session is only read; the stop
sequence closes it after the stop command, with the invocation's job.

Sign-out: Windows does not end session-0 processes that hold a copy of a
signed-in session's token when that session signs out, so the manager does.
The logoff notifications the user host already receives, from the service
control manager and the session poll, are passed to the system manager first;
lock, unlock, disconnect and reconnect are not logoffs. An active invocation
on that session's logon is stopped as a watchdog failure is: `ExecStop`, the
job, the account session, then `Restart=` decides on a failed exit.
A start that is still waiting for exit, readiness or publication has its job
killed and cannot become active; a `Type=oneshot` start whose session signs out
before it completes fails even when its process exited 0. A completed
`RemainAfterExit=yes` oneshot owns no process, so a later sign-out leaves it
active. The reason,
`session <N>, whose logon this invocation used, signed out`, is in status and
the journal. Liveness is the token query and the SID comparison only: the
logon session in LSA and the loaded profile outlive the sign-out.

Limits. A logoff is missed only if the notification is dropped and Windows
reuses the session ID within one poll interval; the invocation then keeps its
logon until its next restart, a weakness user managers share. At machine
shutdown users sign out before the service's preshutdown, so such a unit may
get a sign-out stop and one S4U start before the ordered stop. A `User=`
written as a name is resolved before every start and may ask the domain; a SID
spelling does not depend on that lookup.

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
- **A session logon's process inside the user's session.** Handles are not
  inherited across sessions and the process could not share the daemon's job,
  as the user-manager launch records; Windows would also end it at sign-out
  without `ExecStop`. The copy of the token moved to session 0 keeps the
  launch above unchanged.

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
  removal. All passed ([evidence](R4-EVIDENCE.md#system-units-with-user)).
- For `UserLogon=`: portable tests of the directive's rules, the capability,
  session order and SID matching over a fake native layer, the full-token
  rule and handle ownership, the fallback and its composed errors, status,
  the sign-out stop with `Restart=` and duplicate logoffs, a sign-out during a
  start, and the logoff hook. Windows tests that run only as LocalSystem with
  the test account signed in check the process in session 0 in its unit job,
  that a failed linked token falls back to S4U, and that an `ExecStop` on the
  invocation's token runs as the account in session 0 and leaves the
  invocation running when it ends. A native run on a domain pair with the
  controller off follows before release.
