# Windows identity and manager qualification

September 14, 2026: R2/R3 dependencies are satisfied. R4.3 SCM/maintenance
technical acceptance is complete. The broader R4 launch/session/security/linger
matrix remains open. This ledger distinguishes implemented behavior and accepted
native observations from the remaining qualification; older checkpoint wording
in other evidence documents describes status at the time of those runs.

## SCM readiness and combined maintenance acceptance

The [maintenance contract](OPERATIONS.md#global-maintenance) closes admission
across system and user managers, retains one deadline per accepted attempt and
keeps unfinished cleanup owned. Both listeners must open before SCM readiness;
invalid workload configuration leaves control available for diagnosis and repair.

Two disposable Windows Enterprise LTSC build 26100 qualifications exercised the
real SCM service and an admitted interactive standard user. Exact-source CI
passed, and the merged trees match their tested sources:

| Source / PR | CI | SCM ready | Stop during pending notify boot | Combined maintenance | Equal-tree merge |
| --- | --- | --- | --- | --- | --- |
| `5443e4285a7fed74c7633a5a6224308ce50b78d1`, [#128](https://github.com/PLN/winunitd/pull/128) | [34704903915](https://github.com/PLN/winunitd/actions/runs/34704903915) | 0.281s | 0.068s | 0.049s | `a81abe7fd28ad505eb772b3964ca0da458c0f85f` |
| `d173bf4c0893fa1cb679c8915efe808ca170d5df`, [#136](https://github.com/PLN/winunitd/pull/136) | [34709244930](https://github.com/PLN/winunitd/actions/runs/34709244930) | 0.278s | 0.009s | 0.111s | `02625d89fbb07c15f633926c2e256da3dd495b60` |

Both runs verified invalid cold-load repair, system/user quiescence, admission
remaining closed, idempotent maintenance and enabled-workload recovery after SCM
restart. The later run filled all 128 ordinary control connections before using
the independently reserved maintenance pipe. Standard-user control/environment,
manager and broker crash recovery, final logoff/profile release and absence of
resurrection passed. Temporary session-start configuration was restored and the
live pilot was unchanged. Exact artifacts, scripts, results and cleanup evidence
are retained privately; the source and CI identities above were rechecked for
this acceptance ledger.

Subsequent [control saturation qualification](R2-EVIDENCE.md#control-overload-and-reserved-maintenance-acceptance)
at `90a2689` ran ten times per SYSTEM/headless-user identity against an actual
Windows workload, confirming quiescence through the reserved endpoint while
ordinary connections remained occupied. That later fixture uses loopback
transport; it complements the real named-pipe/SCM run above. The completed
[coordinator audit](R2-COORDINATOR-AUDIT.md) covers admission, deadline ownership,
late native completion, reserved capacity and bounded independent shutdown.

This delivers R4.3. It does not close the broader R4 session/security matrix,
the full MSI servicing contract, or the R7 migration/soak gate.

## Concurrent local headless users

Source `7396beefb88b4400ff10842bdc86f5cfd433cb38` passed
[CI 34772550381](https://github.com/PLN/winunitd/actions/runs/34772550381).
The immutable Windows/amd64 artifact, module and vendored dependency hashes
were verified before a disposable Enterprise LTSC build 26100 run. Its
[PR #212](https://github.com/PLN/winunitd/pull/212) merge
`912d84197ed2211704d9a36f537758fc1bc03ee6` has the tested source tree.

Two distinct local standard users ran concurrently through the real SYSTEM
broker's explicit S4U linger path. One had an existing profile; the other had
never had a profile. Windows created and loaded that profile during launch.
Native process token owners, session 0, one manager and one private desktop
helper per SID, and accepted `headless-s4u` instance identities were checked.

Four workload processes, spanning initial launch and one manager crash per
user, each verified:

- Successful status through their own user control pipe.
- Actual access-denied errors opening the other user's control pipe and private
  profile file, and the system control and reserved maintenance pipes. A timeout
  or missing endpoint would fail these assertions.
- Nonadministrator token identity, target USERPROFILE/LOCALAPPDATA/APPDATA and
  TEMP/TMP matching Windows known folders, plus writable target HKCU.

| Native transition | Observation |
| --- | --- |
| First user's manager crash | Replacement manager and workload ready with completed access/environment checks in 7.201s. |
| Second user's manager crash | Replacement manager and workload ready with completed checks in 10.228s. |
| Each independent recovery | The peer retained its manager PID/instance and workload PID/invocation. The crashed manager's old workload exited. |
| Revoke first linger grant | The second user retained the same manager and workload identities. |
| Revoke final grant | Both managers/helpers/workloads exited, both profiles unloaded, and no user instance returned through the following reconciliation interval. |

The run retained 63 ordered system ownership snapshots. Final cleanup removed
the fixture units, temporary account and newly created profile, restored the
original broker payload/SCM owner, and verified zero remaining linger grants or
fixture processes. Raw artifacts, scripts, snapshots, results and ACL backups
are retained privately.

The first attempt correctly reported `CreateProcessWithTokenW: Access is denied`
because the protected lab executable permitted only the original account to
execute it. That incomplete attempt was retained separately. The accepted run
granted read/execute on the required fixture paths to the appropriate test SIDs,
then restored all five original filesystem security descriptors exactly. No
administrator membership or production permission-policy change was required.

This qualifies the stated two-user headless, local-profile, access-isolation and
independent recovery/revocation scenarios. It does not qualify multiple
interactive sessions, UAC-filtered administrator tokens, redirected profiles,
network credentials, arbitrary inherited handles or the complete R4 gate.

## Notification isolation across managers and restarts

Source `ab95a799c4c35a74824826dd4ed0e14f295425e8` passed
[CI 34815323517, attempt 2](https://github.com/PLN/winunitd/actions/runs/34815323517/attempts/2).
[PR #220](https://github.com/PLN/winunitd/pull/220) merged as
`5217e88e69b9d51ffeb7b05e230e2dcf2c5bcbdc`; both trees are
`85d729930f8bca1f0cf0e6ca08521326527dfbf6`.

Previously, equal notify unit names in independent managers contended for one
global pipe. The native regression reproduces the second start's failure.
Listeners now use the existing invocation ID, isolating managers and giving
restarts a new endpoint. Clients use their injected `WINUNIT_NOTIFY_PIPE`.
The protocol, endpoint DACL and native process/job authorization are unchanged.

The verified immutable Windows artifact ran on disposable Enterprise LTSC
build 26100. Two genuine standard-user S4U managers and SYSTEM ran the same
notify unit name simultaneously. All three sent READY and sustained watchdog
traffic beyond the configured 15-second interval. The run recorded five
workload invocations and 41 ordered ownership snapshots:

| Case | Native observation |
| --- | --- |
| Three initial notify units | Three distinct invocation endpoints; each remained active through at least 71 observed heartbeats. |
| Outside SYSTEM sender | All three live endpoints closed the connection before the acceptance banner. Passing a pipe DACL did not bypass process/job authorization. |
| First and second user-manager crashes | Replacement workloads became ready in 1.192s and 9.626s. Their old processes exited, old pipe addresses were absent and new invocation addresses differed. |
| Peer and SYSTEM stability | The unaffected user and SYSTEM workload retained their PID/invocation identities through recovery; SYSTEM also remained active through both linger revocations. |
| Final cleanup | No owned workload, user manager, helper, loaded test profile or linger grant remained. The new account/profile and fixture units were removed, all five filesystem ACLs restored exactly, and the original broker restored. |

Every user workload also passed its own control call, cross-user/system control
and private-file access denial, target environment/known-folder checks and
writable HKCU. The local native regression passed three repeats, including a
unit stop/restart and rejected READY/WATCHDOG send to the old endpoint. Full
local race tests and exact-source hosted CI passed.

Two incomplete observations are retained with the evidence. Initial CI hit
an existing journal test's one-second recovery wait; that test passed 30 local
race-enabled repeats with `GOMAXPROCS=1` and the full CI retry. The first lab
fixture queried user control before READY during enabled-unit boot, when that
listener was not yet available. It restored the guest but did not qualify the
restart case. The accepted fixture sends READY before those control queries.
User control availability during pending boot remains a separate follow-up.

This closes the reproduced notification-name collision and its stated restart
cases. It does not close the remaining interactive/session matrix or R4 gate.
Exact artifacts, scripts, observations and restoration evidence remain private.

## Current implementation and remaining identity matrix

| Work package | Delivered behavior and evidence | Remaining qualification |
| --- | --- | --- |
| R4.1 Launch context | Interactive `CreateProcessAsUser` launch disables handle inheritance, obtains the target environment/known folders and retains a Windows-owned profile handle. Suspended creation and job ownership precede execution. Earlier genuine SYSTEM-to-interactive launch/crash/logoff observations are recorded in the [admission contract](USER-ADMISSION.md#implementation-evidence-and-limits). | Complete environment/profile and session matrix, including unsupported/redirected/unavailable profiles and cross-session security boundaries. |
| R4.2 User host | Protected explicit/delegated admission, per-SID overrides, session reconciliation, fresh-token recovery, bounded backoff and cancellation are implemented. [Policy/admission qualification](R2-EVIDENCE.md#user-policy-and-cleanup-admission-acceptance), [real S4U snapshot/recovery](R2-EVIDENCE.md#system-user-host-snapshot-qualification), [two concurrent headless users](#concurrent-local-headless-users) and [cross-manager notification isolation](#notification-isolation-across-managers-and-restarts) cover their stated scopes. Dedicated admission administration commands and installer controls remain separate work; protected file administration is available. | Multiple sessions for one SID, concurrent interactive users, rapid real transitions, policy changes during native launch and shutdown, and consolidated no-resurrection evidence. |
| R4.4 Security | Protected policy and pipe checks, bounded impersonation workers and reparse-point rejection are implemented. [Pipe failure handling](R2-EVIDENCE.md#september-12-pipe-impersonation-failure-handling), native launch regressions and [genuine cross-user/system pipe denial](#concurrent-local-headless-users) cover selected paths. | Interactive/filtered-token cases, privileged paths/reparse attacks and inherited-handle checks under the required identities. Same-caller or delayed-adapter tests alone do not establish those boundaries. |
| R4.5 Linger | Headless local-account S4U launch uses `CreateProcessWithTokenW(LOGON_WITH_PROFILE)` and an exclusive private desktop helper. Windows owns profile lifetime; the old manual `LoadUserProfile` path is removed. [Production crash/recovery](R3-EVIDENCE.md#managed-bound-dependent-cleanup), [user-manager replacements](R2-EVIDENCE.md#system-user-host-snapshot-qualification) and [concurrent existing/first-created profiles](#concurrent-local-headless-users) verify profile/process cleanup in their scenarios. | Complete explicit mode/profile/session matrix and credential limitations. These observations do not qualify network authentication or domain/cloud/roaming profiles. Unqualified credential-store modes remain outside supported claims. |

The R4 exit gate still requires the complete real-identity matrix. Hosted
administrator tests, simulated session events and consumer evidence cannot
substitute for missing disposable SYSTEM/session observations.
