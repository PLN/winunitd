# Interactive user admission proposal

Status: proposed, awaiting maintainer decision. This resolves the admission-policy choice in Design v2 and R4.2; it does not change the current implementation.

The current user host starts a manager for each discovered interactive user. The installation-wide default for the first supported release is still undecided. This choice determines which accounts the SYSTEM broker may automatically supervise, the installation experience, migration behavior, and the expected results of session qualification.

| Choice | Installation and logon behavior | Tradeoff |
| --- | --- | --- |
| Explicit opt-in per user (recommended) | Installation enables the system manager. An administrator explicitly enables interactive supervision for each selected user. | Predictable scope on shared machines; an extra setup step for each user. |
| Every interactive user | Each eligible interactive account automatically gets a user manager. | Matches current alpha behavior and needs less setup; installing the service affects other users on the machine. |

## Proposed opt-in behavior

- Persist an administrator-controlled admission list keyed by SID in protected system configuration. An empty list admits no interactive users.
- Enabling a user reconciles any existing suitable session and also applies to future logons. It does not enable workload units automatically.
- Keep one manager per admitted SID, including when that user has several sessions. Do not admit a different account because it reuses a session ID or display name.
- Disabling admission prevents further interactive launches and recovery immediately, then stops any manager authorized solely by interactive admission. Failed cleanup remains visible and retryable; disabling never discards ownership.
- Keep headless linger as a separate, explicit administrator-controlled grant. Interactive admission alone does not authorize boot-time S4U logon. Disabling interactive admission does not silently change an existing linger grant; the interface must show both modes.
- Do not silently create an all-users allowlist when migrating an alpha installation. Require the operator to select intended accounts before activating the new policy.

The administrative commands, configuration schema, and installer selection interface are implementation work after the policy decision. No command described here exists yet.

## Qualification consequences

With opt-in selected, the disposable lab must prove that enabled `alice` starts, unlisted `bob` does not, enabling an already logged-on user reconciles correctly, disabling during launch prevents resurrection, and SID/session reuse cannot bypass admission. Separate tests cover several sessions for one SID, manager crash recovery, logoff, and explicit headless linger.

With all-users selected, the same lifecycle tests apply, but the unlisted-user rejection case becomes an automatic-admission case. Profile/environment construction and cross-user control rejection are required under either policy.
