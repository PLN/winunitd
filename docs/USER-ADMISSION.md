# Interactive user admission policy

Status: policy accepted; implementation and Windows qualification remain R4 work. The current alpha still starts managers for discovered interactive users.

Interactive admission is machine-configurable. The default is conservative: an administrator explicitly enables individual users. An optional machine-wide rule delegates interactive admission to the presence of user unit files. Installing winunitd does not enable that delegation automatically.

| Mode | Admission without an explicit per-user override |
| --- | --- |
| `explicit` (default) | No admission; an administrator must enable the user. |
| `unit-files` (optional) | Admit an interactive user when their own unit directory contains a qualifying unit file. |

An administrator controls the mode and per-SID overrides in protected machine configuration. Each SID may be enabled, disabled, or inherit the mode. Explicit disable takes precedence over file-based admission; explicit enable admits a user even when their unit directory is empty. Missing configuration defaults to `explicit` with no enabled users. Invalid policy must be diagnosed and must never widen admission.

## File-based delegation

- Check the target user's actual unit directory, resolved through their profile and known-folder facilities. Do not derive it from the broker's environment or search other users' directories.
- A qualifying entry is a regular file with a recognized unit filename in that directory. Empty directories, unrelated files, built-in targets, and enablement links alone do not qualify. Do not follow untrusted reparse points to grant admission outside the intended directory.
- Presence admits the user manager; it does not enable or start every discovered unit. Existing unit enablement, dependency, and configuration-validation rules still govern workloads.
- A recognized file with invalid contents may admit the manager so it can report diagnostics. The privileged broker does not parse or execute user commands to decide admission; configuration validation runs in the user manager.
- Probe the directory under the target user's access rights with bounded I/O and admission workers. Access or probe failures do not grant admission and must be visible in diagnostics.
- Reconcile at logon, service startup, policy reload, and through bounded checks for users already logged on. Adding the first unit file should not require another logon. Publish the check interval with the implementation.
- Removing the last file prevents new file-based admission and recovery. It does not abruptly discard or kill an existing invocation: configuration removal follows the normal retained-runtime and explicit-stop rules. An administrator's explicit disable remains the way to revoke admission and request cleanup immediately.

This rule intentionally allows an otherwise unlisted user to opt in by creating their own unit file. That delegation exists only after an administrator selects `unit-files` mode.

## Lifecycle and linger

Persist identity by SID, not account name or session ID. Keep one manager per admitted SID across multiple sessions. Recheck policy and operation generation after asynchronous probes/token lookup and before launch or recovery, including late successful launches.

Explicitly disabling admission prevents further interactive launches/recovery immediately and stops a manager authorized solely by interactive admission. Failed cleanup remains visible and retryable; revocation never discards ownership. A mode change must reconcile existing users through the same lifecycle machinery.

Headless linger remains a separate explicit administrator-controlled grant. Neither a user unit file nor interactive enablement grants boot-time S4U logon. Changing interactive policy does not silently revoke an existing linger grant; diagnostics and administration must show both permissions and the running manager's mode.

Do not silently preserve all-user alpha behavior by populating an allowlist or selecting delegation during migration. The operator must explicitly choose the intended accounts or opt into the file-based rule. Configuration syntax, administrative commands, and installer controls remain implementation work; the mode names above define the policy contract, not an existing CLI.

## Qualification

Prove the default admits no unlisted users; explicitly enabled `alice` starts; unlisted `bob` does not; and explicit disable wins over file presence. In delegated mode, test first-file creation during a session, empty/unrelated directories, invalid unit contents, file removal while a workload lives, inaccessible directories, reparse points, and probe timeout.

Under both modes, cover enabling an already logged-on user, disable or policy change during launch, stale probes, SID/session reuse, multiple sessions, manager crash recovery, logoff, cross-user control rejection, and separate headless linger grants. Skipped SYSTEM/session tests do not count as qualification.
