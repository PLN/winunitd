# Windows identity and manager qualification

September 19, 2026: R2/R3 dependencies are satisfied. R4.2 user-host and R4.3
SCM/maintenance technical acceptance is complete. The broader R4 launch/security/linger
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
User control availability during pending boot is qualified [below](#user-control-during-pending-boot).

This closes the reproduced notification-name collision and its stated restart
cases. It does not close the remaining interactive/session matrix or R4 gate.
Exact artifacts, scripts, observations and restoration evidence remain private.

## User control during pending boot

Source `fdf9d2f6ffabde1e836b079289a02b75071535be` passed
[CI 34817416447](https://github.com/PLN/winunitd/actions/runs/34817416447).
[PR #222](https://github.com/PLN/winunitd/pull/222) merged as
`503f9beee972256ebf07257a119d0679e8876fca`; both trees are
`7c6ddceb447eeb101cb53233f8e8d40d4eb6e1b2`.

The user entry point previously waited for default/graphical-session boot
activation before opening control. A notify unit could hide status and stop
through its startup wait, or wait on its own unavailable control endpoint
before READY. User control now opens and serves before activation, matching
the SYSTEM path. Failure to bind prevents boot; a later listener failure
cancels pending boot work.

The local native regression reproduces the missing control pipe before the
fix and verifies activating status plus completed native stop afterward. An
occupied-endpoint regression verifies that no enabled workload starts when
control cannot bind. Both passed five race-enabled repetitions, followed by
the full local race suite and exact-source hosted CI.

The verified immutable Windows artifact ran through the real SYSTEM broker
on disposable Enterprise LTSC build 26100. Both genuine standard-user S4U
managers recovered enabled notify workloads that queried their own control
endpoint before READY. The equivalent preceding-source fixture could not
complete this step. Each replacement also booted a second workload that
never sent READY:

| Pending boot case | Native observation |
| --- | --- |
| First standard user | Own control call succeeded before READY; status identified the activating native PID/invocation. Stop completed in 0.032s. |
| Second standard user | The same checks passed; stop completed in 0.028s. |
| Both completed stops | Native workload processes exited; units became inactive with no main PID or uncertain termination. Pending unit files and enablement records were removed. |

Five notifying and two pending workload invocations, with 41 ordered ownership
snapshots, also covered same-name notification isolation across SYSTEM/users,
stale endpoint removal, unaffected peer/SYSTEM identities, access/environment
checks, independent revocation and no resurrection. Final cleanup released
all fixture processes/profiles/grants, removed the temporary account/profile,
restored all five filesystem security descriptors and restored the original
broker. Raw artifacts, scripts, observations and restoration evidence are
retained privately.

These are native headless boot/control/stop observations. Interactive
transitions are qualified below; the rest of the session/profile matrix and
the R4 gate remain open.

## Repeated real interactive transitions

The same verified immutable `fdf9d2f` Windows artifact and successful
[CI 34817416447](https://github.com/PLN/winunitd/actions/runs/34817416447)
ran a separate disposable Enterprise LTSC build 26100 qualification through
the real SYSTEM broker. Source, merge and equal-tree identities are recorded
[above](#user-control-during-pending-boot). A fresh local standard account
logged on through Windows; no simulated session events or headless token
launches substituted for these transitions.

Four genuine logons produced seven workload invocations and 122 ordered
ownership snapshots. Every observed workload verified its native owner,
nonadministrator token, nonzero session, native logon SID, target environment
and Windows known folders, writable HKCU, and its own activating status before
READY. Initial watchdog traffic continued beyond the 15-second interval.

| Case | Native observation |
| --- | --- |
| Three real logoff/logon cycles | Enabled workload recovery took 6.004s, 4.986s and 5.540s. Each cycle produced a new Windows logon SID, manager instance and workload invocation under the same broker. Both old processes exited; sampled ownership never exceeded one manager. |
| Admission revoke/re-enable | Revocation removed the user process tree while the Windows session/profile remained. Re-enabling admission launched a fresh manager and invocation in that same logon. |
| Manager crash with pending notify boot | The replacement enabled workload exposed its activating status through its own control pipe while withholding READY. |
| SCM stop during pending readiness | The service stopped in 0.805s and both pending native processes exited. Restart created a new broker identity and recovered enabled work in the existing Windows logon. |
| Final logoff and restoration | The profile unloaded, no manager returned during the following 12-second observation, and all fixture processes, enablement, units, account and profile were removed. Original sign-in settings, admission, payload/SCM state and five filesystem security descriptors were restored. No linger grants remained. |

Three incomplete fixture attempts remain in the private evidence: querying
control before delayed automatic service startup, incorrect snapshot schema/
zero-count assumptions, and a logon-SID query through a managed API that omits
logon IDs. Each restored the guest. The accepted fixture waits for SCM Running,
validates the applicable snapshot schema and obtains the logon SID natively.
Exact scripts, artifacts, probes, snapshots and restoration results are retained.

This qualifies repeated single-user interactive transitions, admission changes
in a settled session and shutdown during workload readiness. Multiple-session
and concurrent-user cases and selected profile cases are qualified below.
Policy/shutdown races inside native manager creation remain open.

## Multiple real sessions and concurrent interactive users

The same verified immutable `fdf9d2f` artifact and successful
[CI 34817416447](https://github.com/PLN/winunitd/actions/runs/34817416447)
passed a separate disposable Windows Server 2025 Desktop Experience build
26100.32230 qualification with RD Session Host. The baseline passed native
SYSTEM service, reboot and SCM-stop smoke before the session fixture. This
adds R4 identity evidence; it does not expand the R6 installer OS matrix.

Two fresh local standard accounts established four genuine Windows logons
through certificate-pinned RDP connections. Native WTS tokens established each
session's account SID, distinct logon SID and nonadministrator identity. Six
notify workload invocations and 82 ordered broker snapshots verified manager
and workload ownership, target profile/environment/known folders, writable
HKCU, and activating status through the user's own control pipe before READY.
Both users ran the same unit name with simultaneous watchdog traffic; initial,
owner-session replacement and SCM-recovered workloads remained healthy beyond
the 15-second watchdog interval.

| Case | Native observation |
| --- | --- |
| Two sessions for one SID plus a second user | Three active Windows sessions with distinct logon SIDs retained exactly one manager per account. Adding the second session preserved the first account's manager and workload invocation. |
| Disconnect and reconnect | The original Windows session became disconnected and then active with its logon SID, manager and invocation unchanged. Disconnecting the later non-owning session likewise preserved both users' work. |
| Non-owning session logoff | Real logoff removed that session; the account's owning session and both users' manager/workload identities remained unchanged. |
| Owning session logoff | After a new second session was established, logging off the manager's owning session produced a fresh manager and invocation in the surviving native logon in 6.036s. Both old processes exited; the other user's identities remained unchanged. |
| Admission revoke/re-enable | Revocation removed the first user's managed processes without removing its Windows logon/profile or disturbing the second user. No manager returned during a further 16-second check. Re-enabling admission recovered work in the existing logon. |
| Both-user SCM stop/restart | Stop completed in 0.270s; all four manager/workload processes exited. A fresh broker recovered enabled work for both existing Windows logons with new invocation identities. |
| Final logoff and restoration | Both profiles unloaded and no manager returned during 16 seconds with the broker still running. Fixture accounts/profiles, units/enablement, credentials, client processes and scheduled tasks were removed. Admission, filesystem security descriptors, RDP/firewall settings, client network mode and both broker baselines were restored and verified. No linger grants or default routes remained. |

Incomplete driver checks are retained with their corrections: a directly
launched client inherited guest-agent output handles, an initial cardinality
check kept a one-session default, and PowerShell unwrapped a single-item client
selection. The owned client was disconnected before its completed execution
and lock were released; later clients used isolated SYSTEM scheduled tasks.
Corrected native checks passed before the fixture continued. Exact artifacts,
client provenance, scripts, native probes, ordered snapshots and restoration
results remain private.

This qualifies the stated real multi-session and concurrent-user transitions.
Selected profile cases and native launch overlap are qualified below. The
remaining security/linger checks keep the broader R4 gate open.

## Interactive profile failures and redirection

Source `187fc6f1e761df1be5c871bcea90efce03140051` passed
[CI 34834516120, attempt 1](https://github.com/PLN/winunitd/actions/runs/34834516120).
[PR #226](https://github.com/PLN/winunitd/pull/226) merged as
`f756c3f8c3854b10379bcea26a47c59d5d2ce4c9`; tested and merged trees are
`3dd7f19f1c2d5bdb5ea7497944aa3e7b08224d9f`.

An earlier genuine interactive hive access-denial run prevented manager launch
but exposed no failure reason across 135 broker snapshots: token/known-folder
lookup failed before SID admission. The broker now publishes bounded
[session failures](OPERATIONS.md#immutable-decision-snapshots) independently of
accepted manager ownership. Local race tests, targeted wire/lifecycle/capacity
tests and vet passed before exact-source CI and native qualification.

The verified immutable artifact ran in a fresh fixture on the same disposable
Windows Server 2025 Desktop Experience build 26100.32230 baseline. Native SYSTEM
service/snapshot/SCM-stop checks passed. A standard-user headless S4U manager and
workload verified session zero, native owner, target profile/environment and
HKCU. Removing linger released the manager, desktop helper and workload,
unloaded the profile and left no resurrection during a further 12-second check.

Two concurrent genuine standard-user RDP logons then produced ten interactive
notify workload invocations and 272 ordered broker snapshots. WTS tokens, native
process ownership and workload probes verified the original logon identity,
profile, environment, known folders, HKCU and control before READY. Both users
used the same unit name; recovered invocations stayed healthy beyond the
15-second watchdog interval.

| Case | Native observation |
| --- | --- |
| Unavailable hive | Denying SYSTEM QueryValues on only the owned user's hive made native KEY_READ return access denied. No affected manager launched; the snapshot exposed a token/profile failure for the real session, including after another 16 seconds. Exact security-descriptor restoration recovered work in the original logon and cleared the error. |
| Local known-folder override | Redirecting Local AppData to an owned local directory changed the workload's known-folder/environment values, unit path and TEMP. Restoring both original registry values and their types recovered the original environment in the same logon. |
| Unsupported UNC path | Delegated admission rejected the non-local unit directory and exposed an admission-probe error. No manager launched during the rejection interval. Restoration recovered the original logon and cleared the error. |
| Reparse path | A native junction in the delegated known-folder path was rejected with a visible admission-probe error and no manager launch. Restoring the known folder recovered the original logon and cleared the error. This selected check does not qualify the broader privileged-path attack matrix. |
| Peer isolation and SCM recovery | Each profile case preserved the other user's manager and invocation. Admission revoke/re-enable likewise preserved the peer. SCM stop took 0.261s and released all four managed processes; a new broker recovered both existing Windows logons. |
| Final logoff and restoration | Both profiles unloaded; no manager returned during 16 seconds with the broker running. Accounts/profiles, units, linger grants, disposable credentials and client processes/tasks were removed. Registry values/security descriptors, filesystem ACLs, admission, RDP/firewall settings, client network mode and original payload/SCM states were restored and verified. |

Exact artifacts, scripts, probes, snapshots and restoration results are retained
privately. Two smoke-driver corrections are retained: snapshot needs no JSON
flag, and null arrays must be filtered before counting in PowerShell. These
selected profile observations do not qualify domain/cloud/roaming profiles,
policy/shutdown overlap inside native creation, or the remaining security and
linger matrix. The R4 gate remains open.

## Native launch overlap and user-host acceptance

Source `91d8742f9903fd2ab531176189aabe9714b2f867` passed
[CI 35401199538, attempt 1](https://github.com/PLN/winunitd/actions/runs/35401199538).
[PR #229](https://github.com/PLN/winunitd/pull/229) merged as
`4d111c4c274883db351693450a2f02955ea048cd`; tested and merged trees are
`dbcbf86f5964acb06031fc774135507dc2633a2d`. Local runtime/manager race suites,
repeated overlap tests, vet and staticcheck passed. CI artifact, module and
vendored dependency hashes were verified; native and cross-build artifacts agree.

An offline disposable Windows Server 2025 Desktop Experience build 26100 fixture
ran the exact-source race-enabled runtime test binary as SYSTEM. An unexported
seam holds each real child after native creation and job placement, before
`ResumeThread`; production callers leave it unset. Control snapshots establish
that the decision is accepted during that interval. Native probes verify the
owner, selected session, non-elevated child and one suspended primary thread.
Built-in Kernel-Process ETW independently orders process creation before the
decision and process exit after release for all 25 cases.

| Native path and decision | Result over five fresh runners |
| --- | --- |
| Genuine WTS token, interactive policy revocation | 5/5 passed; launch superseded, child exited, token released once, no reconciliation restart. |
| Genuine WTS token, shutdown | 5/5 passed; accepted launch remained owned until release, followed by complete cleanup and rejection of later logon. |
| Genuine local-account S4U token, linger revocation | 5/5 passed through `CreateProcessWithTokenW(LOGON_WITH_PROFILE)` and the production private desktop helper; profile loaded while held and unloaded after cleanup. |
| Genuine local-account S4U token, shutdown | 5/5 passed; child/token cleanup and profile unload completed with no linger reconciliation restart. |
| Genuine local-account S4U token, expired shutdown deadline | 5/5 passed; the expired call retained the manager, native work and token while the child stayed suspended. Release and a bounded shutdown retry completed cleanup and profile unload. |

There were no skips. An initial test-fixture attempt replaced its self-assigned
broker root between headless cases; later cases failed before native creation.
The final fixture retains one root for the dedicated runner and closes it after
the suite. The failed attempt is retained privately and is not acceptance evidence.

The immutable CI daemon separately passed 12 jittered maintenance observations.
Every trial created an interactive user manager and then quiesced with the user
host closing, zero native work/instances and no surviving daemon process. None
naturally hit supersession. The account/profile, autologon settings, filesystem
ACLs and original service startup mode were restored and independently inspected;
the fixture left no owned test/daemon processes or active controller lock.

The held seam is after the Windows creation API returns, not inside that API.
The existing lifecycle resumes a superseded child before terminating its job,
so its entry point may execute. The deterministic tests establish retained
ownership and cleanup across launch completion; the immutable observations do
not establish natural supersession. These results do not qualify outbound S4U
credentials, credential-store fallback, or domain/cloud/roaming profiles.

Together with the earlier evidence, this completes R4.2 technical acceptance:

| User-host obligation | Consolidated evidence |
| --- | --- |
| Multiple sessions and simultaneous interactive users | [Real session matrix](#multiple-real-sessions-and-concurrent-interactive-users): owner-session recovery, peer isolation and both-user SCM recovery. |
| Repeated real logon/logoff and pending launch | [Interactive transitions](#repeated-real-interactive-transitions) and [control during pending boot](#user-control-during-pending-boot): bounded stop and no return of obsolete invocations. |
| Notification identity across user/SYSTEM managers | [Equal-name and stale-endpoint matrix](#notification-isolation-across-managers-and-restarts): isolated READY/WATCHDOG traffic and replacement endpoint ownership. |
| Unavailable, redirected and unsupported local profile paths | [Profile matrix](#interactive-profile-failures-and-redirection): visible rejection, recovery and peer preservation; final logoff unloaded both profiles with no manager return during 16 seconds. |
| Policy/shutdown while a native launch remains incomplete | The 25 held cases above: accepted decisions, owned late completion, native exit, token/profile cleanup and no reconciliation resurrection. |

R4.1 retains its security and inherited-handle qualification, R4.4 retains its
real-identity security matrix, and R4.5 retains its credential limitations. R4 as
a whole remains open. Dedicated admission administration commands and installer
controls remain separate work; protected file administration is available.

## Native inherited handles and filtered-token authorization

PR [#231](https://github.com/PLN/winunitd/pull/231) qualified source
`0b739a581363c652f8bb0baafae78d4ab9908907` with exact-source
[CI 35435415557, attempt 1](https://github.com/PLN/winunitd/actions/runs/35435415557).
The merge `f02edebab9a1af8ab73686a344c91abe8cb2e7e2` has the same tree,
`300f703fef1cd63e1a92bd22bf81663b0fee611d`. Native/cross artifact manifests,
payload hashes, module hashes and the vendored dependency tree were verified.
Production code is unchanged by this test package.

An offline disposable Windows Server 2025 Desktop Experience build 26100 guest
ran five fresh race-enabled SYSTEM test runners for each native token lane:

| Lane | Native security cases | Selective-inheritance positive controls |
| --- | --- | --- |
| Local standard-user S4U, session zero | 5/5 passed | 5/5 passed |
| Standard-user WTS interactive token | 5/5 passed | 5/5 passed |
| Genuine UAC-filtered administrator WTS token | 5/5 passed | 5/5 passed |

No case skipped. Positive controls deliberately inherited one of two inheritable
file handles and detected only that file by native identity. All 15 production
launches inherited neither broker file sentinel nor the environment canary, had
working standard handles and target known folders, and reported the expected
non-elevated child identity/session. Every headless profile unloaded after cleanup.
Filtered children reported elevation type 3 and deny-only Administrators group
attributes `0x10`; standard children reported type 1 with no Administrators group.

Each child received explicit access-denied results from fresh endpoints using
production system-control, maintenance and other-user ACLs. Its own connection
authenticated from the actual pipe token as owner only, without administrator
or linger authority. SYSTEM health echoes passed on all protected endpoints both
before and after denial. The three fixture accounts/profiles, autologon values,
filesystem grants and original service startup mode were restored and independently
verified. No owned process or controller lock remained. Raw evidence includes the
cleanup failures and their verified recovery; these were not native test failures.

These isolated pipe fixtures do not establish another live user manager's health,
server-owner validation, pipe squatting or privileged filesystem protection. The
sentinels directly qualify file-handle isolation, not every Windows handle type.
R4.1/R4.4 retain the remaining security matrix; A3 and R4 are not closed by this
package. See [probe execution guidance](TEST-LAB.md#native-user-manager-security-probes).

## Current implementation and remaining identity matrix

| Work package | Delivered behavior and evidence | Remaining qualification |
| --- | --- | --- |
| R4.1 Launch context | Interactive `CreateProcessAsUser` launch disables handle inheritance, obtains the target environment/known folders and retains a Windows-owned profile handle. Suspended creation and job ownership precede execution. Earlier genuine SYSTEM-to-interactive launch/crash/logoff observations are recorded in the [admission contract](USER-ADMISSION.md#implementation-evidence-and-limits). [Selected profile failures and redirection](#interactive-profile-failures-and-redirection) now cover unavailable hive access, local known-folder overrides and delegated UNC/reparse rejection. | Complete cross-session security and inherited-handle checks under the required identities; the selected local-profile cases do not qualify domain/cloud/roaming profiles. |
| R4.2 User host | Protected explicit/delegated admission, per-SID overrides, session reconciliation, fresh-token recovery, bounded backoff and cancellation are implemented. [Policy/admission qualification](R2-EVIDENCE.md#user-policy-and-cleanup-admission-acceptance), [real S4U snapshot/recovery](R2-EVIDENCE.md#system-user-host-snapshot-qualification), [two concurrent headless users](#concurrent-local-headless-users), [cross-manager notification isolation](#notification-isolation-across-managers-and-restarts), [control during pending boot](#user-control-during-pending-boot), [repeated real interactive transitions](#repeated-real-interactive-transitions) and [multiple real sessions/concurrent users](#multiple-real-sessions-and-concurrent-interactive-users) cover their stated scopes. Dedicated admission administration commands and installer controls remain separate work; protected file administration is available. | Technical acceptance complete with [native launch overlap and consolidated no-resurrection evidence](#native-launch-overlap-and-user-host-acceptance). R4.1/R4.4 security and R4.5 credential limits remain separate. |
| R4.4 Security | Protected policy and pipe checks, bounded impersonation workers and reparse-point rejection are implemented. [Pipe failure handling](R2-EVIDENCE.md#september-12-pipe-impersonation-failure-handling), [genuine cross-user/system pipe denial](#concurrent-local-headless-users) and [native WTS/S4U/filtered-token probes](#native-inherited-handles-and-filtered-token-authorization) cover their stated scopes. | Live interactive manager cross-user/server-owner checks and privileged paths/reparse attacks remain. The new file-handle and isolated pipe cases do not close that wider matrix. |
| R4.5 Linger | Headless local-account S4U launch uses `CreateProcessWithTokenW(LOGON_WITH_PROFILE)` and an exclusive private desktop helper. Windows owns profile lifetime; the old manual `LoadUserProfile` path is removed. [Production crash/recovery](R3-EVIDENCE.md#managed-bound-dependent-cleanup), [user-manager replacements](R2-EVIDENCE.md#system-user-host-snapshot-qualification) and [concurrent existing/first-created profiles](#concurrent-local-headless-users) verify profile/process cleanup in their scenarios. | Complete explicit mode/profile/session matrix and credential limitations. These observations do not qualify network authentication or domain/cloud/roaming profiles. Unqualified credential-store modes remain outside supported claims. |

The R4 exit gate still requires the complete real-identity matrix. Hosted
administrator tests, simulated session events and consumer evidence cannot
substitute for missing disposable SYSTEM/session observations.
