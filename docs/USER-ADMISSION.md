# Interactive user admission policy

Status: initial runtime implementation present; administration tooling, real SYSTEM-to-user session qualification, and the remaining R4 lifecycle work are pending. Interactive user managers now default to administrator-enabled admission.

Interactive admission is machine-configurable. The default is conservative: an administrator explicitly enables individual users. An optional machine-wide rule delegates interactive admission to the presence of user unit files. Installing winunitd does not enable that delegation automatically.

| Mode | Admission without an explicit per-user override |
| --- | --- |
| `explicit` (default) | No admission; an administrator must enable the user. |
| `unit-files` (optional) | Admit an interactive user when their own unit directory contains a qualifying unit file. |

An administrator controls the mode and per-SID overrides in protected machine configuration. Each SID may be enabled, disabled, or inherit the mode. Explicit disable takes precedence over file-based admission; explicit enable admits a user even when their unit directory is empty. Missing configuration defaults to `explicit` with no enabled users. Invalid policy must be diagnosed and must never widen admission.

## Configuration

The system daemon reads `<base-dir>\user-admission.json` (normally `C:\ProgramData\winunitd\user-admission.json`). Example using a documentation-only SID:

```json
{
  "mode": "explicit",
  "users": {
    "S-1-5-21-1-2-3-1001": "enabled"
  }
}
```

Use actual target account SIDs when administering a machine. Set `mode` to `unit-files` for delegation; omit an override or use `inherit` to apply the mode. Set a user's override to `disabled` to block delegated admission. Neither setting enables workload units.

On Windows the policy must be owned by SYSTEM or Administrators, with no write, delete, ownership, or DACL-change grant to another principal. Read-only access for ordinary users is permitted. Unsafe or unsupported permissions are rejected. Edit or atomically replace the file from an administrator-controlled workflow; dedicated administration commands and installer controls remain pending.

Reads are limited to 64 KiB. Unknown fields/modes/overrides, duplicate keys, invalid SIDs, and trailing JSON are rejected. Startup errors leave the default empty policy. A malformed or inaccessible replacement is diagnosed and retains the last accepted policy; removing the policy file restores the empty default. The daemon checks policy and existing sessions every ten seconds, with reconciliation serialized to prevent overlapping polling loops. Revocation publishes the new policy before bounded cleanup, and cleanup failure is retried on later reconciliation.

## File-based delegation

- Check the target user's actual unit directory, resolved through their profile and known-folder facilities. Do not derive it from the broker's environment or search other users' directories.
- A qualifying entry is a regular file with a recognized unit filename in that directory. Empty directories, unrelated files, built-in targets, and enablement links alone do not qualify. Do not follow untrusted reparse points to grant admission outside the intended directory.
- Presence admits the user manager; it does not enable or start every discovered unit. Existing unit enablement, dependency, and configuration-validation rules still govern workloads.
- A recognized file with invalid contents may admit the manager so it can report diagnostics. The privileged broker does not parse or execute user commands to decide admission; configuration validation runs in the user manager.
- Probe the directory under the target user's access rights with bounded I/O and admission workers. Access or probe failures do not grant admission and must be visible in diagnostics.
- Reconcile at logon, service startup, policy reload, and through ten-second checks for users already logged on. Adding the first unit file does not require another logon. Slow native calls may delay a reconciliation pass.
- Removing the last file prevents new file-based admission and recovery. It does not abruptly discard or kill an existing invocation: configuration removal follows the normal retained-runtime and explicit-stop rules. An administrator's explicit disable remains the way to revoke admission and request cleanup immediately.

This rule intentionally allows an otherwise unlisted user to opt in by creating their own unit file. That delegation exists only after an administrator selects `unit-files` mode.

## Lifecycle and linger

Persist identity by SID, not account name or session ID. Keep one manager per admitted SID across multiple sessions. Recheck policy and operation generation after asynchronous probes/token lookup and before launch or recovery, including late successful launches.

Once the daemon accepts the changed policy, explicitly disabling admission prevents further interactive launches/recovery and stops a manager authorized solely by interactive admission. File edits take effect on the next successful policy check. Failed cleanup remains visible and retryable; revocation never discards ownership. A mode change must reconcile existing users through the same lifecycle machinery.

Headless linger remains a separate explicit administrator-controlled grant. Neither a user unit file nor interactive enablement grants boot-time S4U logon. Changing interactive policy does not silently revoke an existing linger grant; diagnostics and administration must show both permissions and the running manager's mode.

Do not silently preserve all-user alpha behavior by populating an allowlist or selecting delegation during migration. The operator must explicitly choose the intended accounts or opt into the file-based rule before updating a deployment that needs interactive user managers. Existing deployments are not changed merely by updating this repository.

## Admission capacity

The broker retains at most 128 user-manager instances, including failed launches
and uncertain cleanup. New users are rejected as busy at capacity; successful
cleanup releases the slot. Existing users can still be stopped and reconciled.
Interactive mappings and pending session requests are each capped at 4096.
The native WTS enumerator also rejects snapshots above 4096 entries. Oversized
snapshots are errors and preserve previous ownership; they are never truncated
and interpreted as logoffs.

Four native admission workers are shared by logon and linger requests. Session
reconciliation, policy refresh and linger scanning each have one reserved owned
slot. The session listener queues cleanup using four wait workers with one entry
per retained SID. A timed-out native stop remains owned per process, so the
128-instance cap also bounds retained user-process stop attempts. These fixed
limits are implementation bounds; configurable quotas remain future work.

## User-manager recovery

Launch failures and rapid manager exits use per-SID exponential delays of 1, 2,
4, 8, 16, 32 and at most 60 seconds. A minute of successful runtime resets the
delay. Reconciliation checks due recovery on its ten-second cadence; the delay
is an earliest retry time, not a promise of an exact launch time. Known waiting
sessions avoid another token acquisition before that time. Linger token failures
also retain a delayed record, and periodic reconciliation retries enabled linger
records after processing interactive sessions.

Recovery records share the 128-instance limit. Logoff, explicit revocation,
disable-linger when no admitted session remains, and shutdown cancel their
applicable recovery. Failed or pending native cleanup must still complete before
replacement. Each admitted retry obtains a fresh token. Status exposes bounded
per-SID `userRecovery` entries with starting/waiting/cleanup state, the last error
and the earliest retry time when waiting. No separate per-user timer goroutine is
created. Native headless profile/crash qualification remains a separate gate.

## Implementation evidence and limits

The native user-manager launcher uses `CreateProcessAsUser` with handle
inheritance disabled and no parent standard-handle list. Windows supplies the
child's default streams for the windowless launch. Suspended creation and job
assignment still precede execution; failed cleanup retains ownership.
Cross-session children explicitly leave the SYSTEM broker's root job and join
their dedicated kill-on-close job atomically during creation. Windows jobs cannot
contain processes from multiple sessions. The broker owns the dedicated job's
noninherited handle; its death closes that ownership even during launch. Ordinary
unit and user-manager jobs do not permit breakaway. Same-session launches retain
the outer-job assignment. Native isolated tests cover placement, broker-crash
cleanup and rejection of workload breakaway; actual session qualification remains
separate. This uses the Windows 10 / Server 2016
[job-list creation attribute](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-updateprocthreadattribute).
The default environment comes from `CreateEnvironmentBlock` for the target token
with broker inheritance disabled. AppData paths are resolved through the target
token's known-folder APIs, including when the child applies its user environment.
Native regressions check Win32/Go stream writes, absence of broker-only environment
variables, default startup handles, known-folder values, and fail-closed lookup.
The default path verifies token identity and requires a nonempty user-profile
environment before creation; it does not fall back to the broker's environment.
The API constraints are documented by Microsoft for
[CreateProcessAsUser](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-createprocessasuserw)
and [CreateEnvironmentBlock](https://learn.microsoft.com/en-us/windows/win32/api/userenv/nf-userenv-createenvironmentblock).

For interactive sessions, Windows owns the logon's profile lifetime. The broker
requires the user's hive to exist and retains an ordinary registry handle until
process-tree cleanup; Windows also closes this handle on broker death. It does
not add a `LoadUserProfile` reference: disposable guest testing found that such a
reference survived broker death and prevented profile release after logoff,
although ordinary shutdown released it correctly. Missing or not-yet-loaded
interactive profiles fail closed and can be retried by session reconciliation.
The `8a61393` replacement passed the same disposable SYSTEM/user launch and crash
sequence, with profile release and no manager resurrection at final logoff.

The headless path still uses an explicit `LoadUserProfile` reference and duplicated
token with retryable unload after process-tree cleanup. Its abrupt-death profile
ownership is not qualified and remains an R4.5 prerequisite; interactive evidence
does not qualify it. Managed-profile paths currently accept local machine accounts;
domain/cloud/roaming-profile support remains outside their claims.

Portable fault tests cover termination-before-unload, failed unload retry, and
failed creation retaining profile ownership. The remaining native session matrix
and redirected/unloaded-profile qualification remain open. Headless linger
is not qualified by these changes.

Admission snapshots are copied and revisioned. Delayed file probes and launches recheck their session request and policy revision. Explicit revocation respects an independent linger grant; disabling that last grant cannot keep a manager through a revoked interactive session.

Session reconciliation applies successful enumerations as authoritative snapshots:
missing sessions invalidate pending token requests and release interactive ownership.
Remaining sessions are queried before idle-manager cleanup, preserving a manager
when another session for the same SID replaces the old one. Failed enumeration
preserves current ownership. Snapshots overlapping newer logon/logoff, policy,
shutdown or reconciliation decisions are discarded; later passes retry.
Exited interactive managers obtain fresh tokens before recovery, and failed idle
cleanup remains tracked for retry. Independent linger grants remain effective.
This does not yet qualify native SYSTEM/session transitions or provide bounded
concurrent recovery workers and restart backoff.

The Windows probe duplicates the supplied token, impersonates it on a dedicated OS thread, resolves LocalAppData through the Windows known-folder API, and pins directory ancestors against replacement while rejecting reparse points. Four probe workers and a five-second caller deadline bound stalled probes; each pending worker retains its token, handles, and slot until cleanup succeeds. Enumeration is capped at 4,096 entries. Delegation currently rejects UNC and reparse-point paths; redirected-profile support still needs R4 qualification. No user file contents are parsed by the broker.

Portable tests cover default/explicit admission, override precedence, immutable snapshots, stale probes, revocation, file-removal ownership, and separate linger grants. Windows tests cover policy ACLs, missing/empty/unrelated directories, invalid unit contents, junction rejection, impersonation identity, and timed-out workers retaining duplicated tokens and capacity. Junction tests run without a symbolic-link-privilege skip. These tests do not substitute for a real SYSTEM-to-standard-user session run or complete profile/environment qualification.

## Qualification

Prove the default admits no unlisted users; explicitly enabled `alice` starts; unlisted `bob` does not; and explicit disable wins over file presence. In delegated mode, test first-file creation during a session, empty/unrelated directories, invalid unit contents, file removal while a workload lives, inaccessible directories, reparse points, and probe timeout.

Under both modes, cover enabling an already logged-on user, disable or policy change during launch, stale probes, SID/session reuse, multiple sessions, manager crash recovery, logoff, cross-user control rejection, and separate headless linger grants. Skipped SYSTEM/session tests do not count as qualification.
