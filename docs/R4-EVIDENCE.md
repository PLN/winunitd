# Windows identity and manager qualification

September 19, 2026: R2/R3 dependencies are satisfied. R4.2 user-host and R4.3
SCM/maintenance technical acceptance is complete. The broader R4 launch/security/linger
matrix remains open. This ledger distinguishes implemented behavior and accepted
native observations from the remaining qualification; older checkpoint wording
in other evidence documents describes status at the time of those runs.

October 14, 2026: R4.4 and R4.5 are closed for local accounts. R4.1 is closed
for local and domain accounts, including roaming profiles, by the
[domain run](#domain-accounts-and-roaming-profiles). Microsoft Entra (cloud)
accounts are excluded from supported claims.

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
| `5443e4285a7fed74c7633a5a6224308ce50b78d1`, #128 | 34704903915 | 0.281s | 0.068s | 0.049s | `a81abe7fd28ad505eb772b3964ca0da458c0f85f` |
| `d173bf4c0893fa1cb679c8915efe808ca170d5df`, #136 | 34709244930 | 0.278s | 0.009s | 0.111s | `02625d89fbb07c15f633926c2e256da3dd495b60` |

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
CI 34772550381.
The immutable Windows/amd64 artifact, module and vendored dependency hashes
were verified before a disposable Enterprise LTSC build 26100 run. Its
PR #212 merge
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
CI 34815323517, attempt 2.
PR #220 merged as
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
CI 34817416447.
PR #222 merged as
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
CI 34817416447
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
CI 34817416447
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
CI 34834516120, attempt 1.
PR #226 merged as
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
CI 35401199538, attempt 1.
PR #229 merged as
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

PR #231 qualified source
`0b739a581363c652f8bb0baafae78d4ab9908907` with exact-source
CI 35435415557, attempt 1.
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

## Live normal-client and read-only filesystem observations

The same immutable source `0b739a581363c652f8bb0baafae78d4ab9908907`
and verified CI payload were installed in an offline disposable Windows Server
2025 Desktop Experience build 26100 guest. Two standard users held distinct real
RDP logons, each with its production user manager and notify/watchdog workload.
Each manager ran the production `winctl --user snapshot` as a temporary oneshot
unit. Both operations succeeded. Across these calls and a subsequent 16-second
watchdog interval, both manager instances, workload invocations, native process
identities and logon identities remained unchanged, and heartbeats advanced.
The temporary units were removed. This establishes successful normal client
connections, including acceptance of the expected server owner; it does not
establish rejection of an unexpected server owner or another user's caller.

A bounded read-only inspection covered 15 objects: the installation root, daemon
and client binaries, and the current data tree. All had SYSTEM/Administrators
ownership and no Allow ACE granting mutation rights to an untrusted SID. No
reparse point was followed. These are observations of the prepared fixture's
configuration, not evidence of unsafe-root rejection or reparse-race resistance.

The two accounts/profiles, client tasks/connections, temporary network setting,
credentials, RDP settings, admission policy, filesystem grants and original
service payload/state were restored and independently checked. Raw evidence
retains two corrected harness assumptions: obsolete `snapshot --json` syntax and
an operation-state assertion expecting `completed` instead of `succeeded`.
Neither result is counted as a passed check. No production code changed.

Live cross-user rejection, unexpected-server-owner/pipe-squatting cases and
privileged-path/reparse manipulation were not executed in this continuation.
The wider R4.1/R4.4 security gates remain open.

## Live manager cross-user rejection and production-client server-owner validation

The live qualification used immutable source
`344bf2d2e970a347b71f03c0da0ab456324f9f85`, whose source tree equals merged main
(45e374a). The binaries came from successful
CI run 35987962097,
with Windows artifact `10803655676` and cross-build manifest `10802544533`.
The qualified tree is `629a6bd7d9b39d2d3c478aec38fe9994293e8c36`;
the admitted binaries were unchanged.
Preparation exposed the daemon-log startup regression #253,
fixed and separately qualified in #255.

Two standard callers and a filtered administrator used genuine interactive
logons with native token/session checks. Each caller opened its own live manager
pipe successfully, then attempted the peer manager and SYSTEM control and
maintenance endpoints. Independent server identity and existence checks bound
the negative observations to live endpoints. Production commands were also
checked, and legitimate manager/workload identities and advancing heartbeats
were observed across intervals of at least 16 seconds.

For server validation, a private fixture held each canonical endpoint only while
the legitimate listener was absent. The unmodified production client connected
to the independently identified unexpected server. Each connection ended with
an error, empty standard output and zero payload bytes. Listener exit and
independent endpoint absence were verified before broker restart; bounded
readiness checks preceded normal-client and stable-health positive controls.

| Case | Observed result |
| --- | --- |
| First standard caller | PASS: own manager open succeeds; peer, control and maintenance native opens return access denied (WinError 5); production system commands fail with endpoint-specific denial. |
| Second standard caller | PASS: the same own/peer/system checks pass under a distinct genuine logon. |
| Filtered administrator | PASS: native token checks confirm deny-only administrator membership; own access succeeds and peer/system access is denied. |
| Unexpected user-endpoint server | PASS: the production client fails closed when server-identity inspection is denied; no payload is sent. This is not an explicit owner-mismatch result. |
| Unexpected control-endpoint server | PASS: the production client explicitly rejects the wrong server owner and sends no payload. |
| Unexpected maintenance-endpoint server | PASS: the production client explicitly rejects the wrong server owner and sends no payload. |

All six final cases passed their evidence validator with immutable archive and
prerequisite bindings. A failed native open alone was not accepted as proof of
client server-owner validation. The results qualify live cross-user rejection,
filtered-token denial and production-client fail-closed server validation for
these endpoints. Fixture accounts, sessions, grants, tasks, credentials and
configuration were removed or restored; independent verification confirmed the
baseline broker and normal client operation, with no fixture or wrapper lock
remaining. Raw evidence, including unsuccessful fixture attempts and their
corrections, remains private.

Privileged-path mutation protection beyond the installer preflight, unsafe-root
install/repair variants, reparse races and non-file handle classes were not
exercised. Earlier file sentinels and
intentional standard-I/O handles do not cover every socket, pipe, event, mutex,
semaphore, section, registry, process, thread, token or job handle. It is not soak
evidence or a resolution of recovery-backoff #254.
R4.4 remains unchecked; R4.1/R4.5 and the remaining release
prerequisites are unchanged, and these results do not authorize R7 handoff.

## Privileged-path protection: runtime mutation attempts and unsafe-root install/repair

Case C of the live security qualification covered privileged paths. The
package was built with `packaging/wix/build.ps1` from a clean checkout of
merged main `b290c63f2d6cf0485ce651eb16407b467b359fd1`, tree
`e811c80a832e6d7ff0fdac173c1cdaa64c4ac00d`, which passed exact-source
CI run 36068345856.
CI does not publish the MSI. The payload module and vendor manifests match the
CI artifacts, but the locally built executables are not byte-equal to the CI
executables; the package is therefore a local build from the CI-green source
tree, not a package of CI artifact bytes. Both hash sets are retained privately.
The pinned package manifest records a clean build of installer version 0.1.0.

Two genuine interactive callers, a standard user and a UAC-filtered
administrator, each ran 67 operations from their own logon session at medium
integrity. A 64-bit `asInvoker` helper was used with no thread impersonation
token and with UAC file virtualization disabled. Eight positive controls ran in
caller-writable control directories. The other 59 operations attempted create, write,
append, same-parent rename and delete on bounded inert sentinels in the machine
data root, `units`, `enabled`, `default.target`, `linger`, `daemon` and the
installed `bin` directory. `default.target` received directory-only operations;
the full set ran in an inert child directory that the unit loaders ignore. A
SYSTEM observer recorded each sentinel's identity, content hash and security
descriptor before and after. Manager and workload identities stayed stable and
heartbeats advanced across 16-second intervals.

Install and repair were then attempted against four unsafe-root fixtures: a
data root owned by an untrusted user, a data root granting write access to a
non-administrator, and a junction at the immediate `units` or `linger` child.
Each shape ran in both install and repair, with plain `msiexec` servicing
commands and no test-injection properties. One repair lane kept the service
running; the others used a stopped service for byte stability. The pinned WiX
sequence places the service preflight before service stop and file
installation, so these are static preflight refusals, not rollback results.
Finally, a registration using the packaged image path and LocalSystem with a
custom base-directory argument was presented to install and repair.

| Lane | Observed result |
| --- | --- |
| Standard caller, runtime mutation | PASS: 8 positive controls succeed; all 59 protected-tree operations return access denied (WinError 5). Sentinel identity, bytes and descriptors, unit and enable listings and workload health are unchanged; no virtualized write occurs. |
| Filtered administrator, runtime mutation | PASS: the same 8 controls and 59 access-denied results under deny-only administrator membership. |
| Untrusted-owner data root, install | PASS: MSI exit 1603 with the expected root-ownership preflight conflict. |
| Untrusted-owner data root, repair (service running) | PASS: exit 1603 with the root-ownership conflict; the running service keeps the same process identity and SCM configuration. |
| Non-administrator write grant on data root, install | PASS: exit 1603 with the expected root write-grant conflict. |
| Non-administrator write grant on data root, repair | PASS: exit 1603 with the root write-grant conflict. |
| `units` junction, install | PASS: exit 1603 with the expected reparse-point conflict; the junction and its external target are unchanged. |
| `units` junction, repair | PASS: exit 1603 with the reparse-point conflict; junction and target unchanged. |
| `linger` junction, install | PASS: exit 1603 with the expected reparse-point conflict; junction and target unchanged. |
| `linger` junction, repair | PASS: exit 1603 with the reparse-point conflict; junction and target unchanged. |
| Custom base directory, install | PASS: exit 1603 with the custom-base conflict; the base is neither adopted nor relocated. |
| Custom base directory, repair | PASS: exit 1603 with the custom-base conflict; SCM configuration and both trees are unchanged. |

Each refusal matched the exact diagnostic expected for its shape; any other
preflight conflict would have failed the lane. After every refusal the service
configuration and state, product registration, Installer cache, installed
tree, transaction state files, `PATH`, event source and product registry
matched the pre-attempt inventory. In the running repair lane only
product-owned daemon, journal and runtime files were allowed to change size or
content. All twelve lanes passed hashed acceptance with immutable archive and
prerequisite bindings. Complementary `tools/msi-check` source tests from the
same commit passed both symlink tests under SYSTEM; see the observations below
for the user-owner test.

These results qualify runtime denial of the tested create, write, append,
rename and delete operations on the tested protected trees for standard and
UAC-filtered interactive callers, and static install and repair refusal of
untrusted-owner, unsafe-ACE, immediate `units`/`linger` junction and
custom-base fixtures. They do not qualify reparse races, recursive
descendant-junction handling, ancestor ACL policy or inherited-handle isolation
beyond the tested file handles. Case C used local accounts only;
domain/cloud/roaming profiles remain under R4.1. Cross-parent moves, other
loader paths and non-file handle classes were not exercised. It is not soak
evidence, does not close R4.4 or other R4/R7 prerequisites and does not
authorize R7 handoff.

Two observations were recorded without a product change:

- Uninstall intentionally retains the `HKLM\SOFTWARE\PLN\winunitd` `DataRoot`
  value, authored as the key path of the permanent `DataRoot` component in
  [Package.wxs](../packaging/wix/Package.wxs), alongside the retained data
  directories described in
  [INSTALLATION.md](INSTALLATION.md#data-kept-across-repair-upgrade-and-remove).
  Returning the lab to its pre-install baseline required removing that value
  separately; uninstall retention was not changed.
- `TestInspectDirectoryRejectsUserOwner` assumes `t.TempDir()` is owned by an
  untrusted user. Under SYSTEM the temporary directory is trusted, so the test
  observes no conflict and fails. It passes under a user token and in CI. This
  is a test-fixture identity assumption, not a product finding; the SYSTEM
  failure is retained, not counted as a pass.

Fixture accounts, sessions, grants, junctions, owner and ACE changes, SCM
arguments and the package were removed or restored, and independent
verification confirmed the original service configuration, broker and normal
client operation. Raw evidence, including unsuccessful fixture attempts and
their corrections, remains private.

## Residual path and handle classes

Case C and #231 left four classes
open: reparse races, recursive descendant junctions, ancestor ACL policy and
inherited handles other than files. In October 2026 the maintainer reviewed
the source, asking for each class who could create the shape and what a
privileged component does with it. Only administrators and SYSTEM are trusted
in this model; an administrator can already replace the installed binaries.

| Class | Who can create the shape | Result |
| --- | --- | --- |
| Reparse races and links below a data directory | The data root's protected DACL is inherited by everything below it. Case C showed every create, rename and delete by standard and UAC-filtered callers in the protected trees returning access denied. | Only administrators can create the shape. |
| A data directory created before the data root is secured | A standard user may create entries in `%ProgramData%`. On a first install, `winunitd` or one of its data directories could be created between the package preflight and `CreateFolders`. | The package sets the data root's owner and protected DACL. Each time it starts, the system manager checks the data root and `%ProgramData%` on open handles (the compatibility-floor chain), and since #295 also `units`, `enabled`, `journal`, `runtime`, `linger` and `daemon`. An unsafe one holds admission. |
| Recursive descendant junctions | A user-created entry stays owned by that user, and a user can create entries only where a directory is user-owned or user-writable. Such a data directory, or such a data root, fails the startup check or the package preflight. | Not reachable through a checked tree. |
| Ancestor ACL policy | `%ProgramData%` is checked on every start: a trusted owner, and no one else may delete, rename or re-permission its entries. The package refuses an install directory or data root with an ancestor that is a reparse point. The volume root above `%ProgramData%` is not checked; by default Windows does not let users rename or replace `%ProgramData%`. | Changing those ancestors needs an administrator. Documented, not checked. |
| Per-user paths read by SYSTEM | The broker opens a user's `units` directory only to learn whether unit files exist. It impersonates that user and rejects reparse points. Every other per-user file is read and written by the user's own manager under the user's own token. | No privilege boundary is crossed. |
| Inherited handles other than files | Interactive managers start through `CreateProcessAsUser` with handle inheritance off. Headless managers start through `CreateProcessWithTokenW`, which has no inheritance parameter. Workloads start with `PROC_THREAD_ATTRIBUTE_HANDLE_LIST` holding only their standard handles. | Windows decides inheritance per handle-table entry, whatever the object type. So #231's native file sentinels, which no production launch inherited in the S4U, standard WTS and filtered WTS lanes, exercise the same mechanism every handle type goes through. |

With #295 merged, the four classes are closed for local accounts on a machine
whose administrators are trusted. Domain, cloud and roaming profiles remain
under R4.1.

## Headless linger and credential limits

On October 14, 2026 a disposable Windows 11 Enterprise LTSC guest (build 26100)
ran the MSI built from `8f24b84` (0.1.1-alpha). A local standard user was
admitted for an interactive manager and later given linger. One user service
sampled every 30 seconds what its token could reach: loopback TCP; an SMB 3.1.1
share on the lab network with encryption required, once with the logon's own
credentials (the server's credential was stored in Credential Manager) and once
with an explicit user and password given to `net use`; DPAPI `CurrentUser`
protect and unprotect; a generic Credential Manager entry; an existing
EFS-encrypted file and a new one. The secrets were created in the user's own
signed-in session. Sign-ins were genuine RDP sessions over loopback.

| Step | Observed |
| --- | --- |
| Fresh install | Service running with no admission hold; the #295 data-directory check passed on a clean tree. |
| Sign-in | Interactive manager in the user's session. |
| `enable-linger` while signed in | The running manager kept `mode=interactive`. |
| Sign-out while lingering | The interactive manager and its service ended with the session. The broker started a `headless-s4u` manager in session 0, and the enabled service sampled again 5 seconds after sign-out. |
| Two cold boots, nobody signs in | The `headless-s4u` manager started at boot; the service's first sample came about 20 seconds after boot. |
| Sign-in while the headless manager runs | Same manager and service, `mode=headless-s4u` with one interactive session; no second manager. |
| Sign-out | Manager and service unchanged. |
| `disable-linger` | Manager and service stopped. |
| Profile after `disable-linger` | With no service running, the user's registry hive unloaded within two minutes. After a service that ran only the two EFS checks, it stayed loaded for over seven minutes, until the guest was shut down; with the full probe it also stayed loaded across a restart of the `winunitd` service. No process of the user remained in either case. |

| Capability | Signed-in session or interactive manager | Headless, nobody signed in since boot | Headless while the user is signed in |
| --- | --- | --- | --- |
| Loopback TCP | Yes | Yes | Yes |
| SMB with the logon's own credentials | Yes | Access denied | Access denied |
| SMB with an explicit user and password | Yes | Yes | Yes |
| DPAPI `CurrentUser` protect and unprotect | Yes | No | Yes, and briefly after sign-out (gone within 40 seconds) |
| Credential Manager read | Yes | No (`ERROR_NO_SUCH_LOGON_SESSION`) | No |
| Read an existing EFS file | Yes | No | Yes, and for as long as observed after sign-out (up to 95 seconds) |
| Create an EFS file | Yes | No | No |

The S4U token carried the `NETWORK` logon group, not `INTERACTIVE` or `BATCH`.
The DPAPI and EFS successes in the last column come from keys Windows caches
while the user is signed in; a service cannot depend on them. Windows also
reuses an SMB session authenticated with explicit credentials for later access
to the same server name from the same logon session. After a service maps a
share with a password, its other accesses to that server therefore succeed as
well; the probe used a second name for the server in its explicit check to keep
the two apart.

For local accounts this closes R4.5. Lingering managers behave as documented, a
headless service reaches the network only with credentials it supplies itself,
and DPAPI, Credential Manager and EFS are not available to it. The optional
credential-store mode (`headless-store-uri`) was not exercised; it stays
experimental and outside supported claims. Domain, cloud and roaming profiles
remain under R4.1.

## Domain accounts and roaming profiles

Until October 2026 both user-manager paths refused accounts from outside the
local machine. The check was left over from the time winunitd loaded profiles
itself with `LoadUserProfile`, which needs the roaming profile path for domain
accounts. Windows now loads the profile in both modes, so
#297 removed the check.

On October 14, 2026 a lab domain ran the MSI built from that change
(0.1.1-alpha): one Windows Server 2025 evaluation domain controller and a
domain-joined Windows 11 Enterprise LTSC guest (build 26100). Two standard
domain users were admitted, one with a local profile and one with a roaming
profile on a share of the domain controller. The probe service from the
[linger run](#headless-linger-and-credential-limits) ran again with one more
check: reading a file on a domain controller share with the logon's own
identity. Sign-ins were genuine RDP sessions over loopback.

| Step | Observed |
| --- | --- |
| Sign-in | Interactive manager in the user's session; the service's token authenticated with Kerberos. |
| `enable-linger` while signed in, then sign-out | The broker started a `headless-s4u` manager through Kerberos S4U; the enabled service ran again within 15 seconds. |
| Cold boot with the domain controller running | Headless manager at boot; the service's first sample came 56 seconds after boot. |
| Cold boot with the domain controller off | No manager. `winctl status` showed the user waiting, the S4U error ("your domain isn't available") and the next retry time. |
| Domain controller started again | A later retry started the headless manager about two minutes afterwards, without intervention. |
| Sign-in and sign-out beside the headless manager | The same manager carried on; no second manager started. |
| `disable-linger` | Manager and service stopped. |

The headless service reached what a local account's service reaches: loopback
TCP, and a share mapped with credentials it supplied itself. The domain
controller's share was denied, because an S4U logon has no credentials for
outbound authentication. Windows documents Kerberos constrained delegation as
the exception; winunitd does not configure it, and that setup was not tested. DPAPI,
Credential Manager and EFS behaved as in the local-account run.

The roaming-profile user wrote a marker file into its roaming AppData at each
stage, and the server's copy of the profile was checked after each step.

| Step | Observed |
| --- | --- |
| Sign-in | Profile downloaded; Windows reported it as roaming. |
| Sign-out with linger enabled | Profile uploaded to the server as usual, then the headless manager started. |
| While the headless manager ran | Windows reported the loaded profile as local. A sign-in beside it neither downloaded nor uploaded the profile, so a change made in that session stayed on the machine. |
| `disable-linger` | The hive unloaded; nothing was uploaded. |
| Next sign-in and sign-out | The profile roamed again. The change made during linger was kept and uploaded. |

No change was lost, but while linger holds a roaming profile, the profile stops
roaming on that machine. Changes made on other machines do not arrive there,
and a later upload follows Windows' newest-file rule. A user who signs in on
several machines can therefore lose settings, the registry hive in particular,
because it roams as one file. As in the local-account run, a lingering service
that attempted EFS kept the hive loaded after `disable-linger`; until the next
reboot that also keeps the profile from roaming.

This closes R4.1 for local and domain accounts, including roaming profiles.
Microsoft Entra (cloud) accounts and folder redirection were not tested; Entra
accounts are excluded from supported claims.

## Units placed by SYSTEM in a lingering user's manager

On October 15, 2026 the procedure in
[running a user's units from SYSTEM](RUNTIME-REFERENCE.md#running-a-users-units-from-system)
ran on the disposable Windows 11 Enterprise LTSC acceptance guest, with an MSI
built from `565af36` (#18, #19 and #20 together). Every step ran as LocalSystem
through the guest agent, for a new local standard user who never signed in.
The unit ran `cmd.exe`, which started a child.

| Step | Observed |
| --- | --- |
| `enable-linger` | A `headless-s4u` manager in session 0. `winctl --user=<user> status` answered at the first attempt and reported the unit directory under the user's profile, which already existed. |
| Unit file under an ignored name | `daemon-reload` loaded the same units as before; the `.service.tmp` file was not listed. |
| Renamed to `.service` | Owned by Administrators, with access inherited from the profile, the user's included. Reload accepted it. |
| `enable`, `start` | The unit ran as the user in session 0. |
| `unit-of-process` on the user pipe | The main process with `main=true`, its child with `main=false`, both with the unit's invocation. |
| `unit-of-process` on the system pipe | `not-found`, exit 4. |
| Go client as SYSTEM | `Dial` and `DialUser` passed the server-identity check, both endpoints listed `unit-of-process`, and the creation times matched those read through the probe's own handles. |
| Cold boot, nobody signed in | The headless manager started and the enabled unit ran again, its main process 15 seconds after boot, with a new invocation. Attribution as above. |
| Removal | `stop`, `disable`, deleting the file and `daemon-reload` removed the unit and its processes. `disable-linger` ended the manager and its pipe. |

## System units with User=

On October 15, 2026 [`User=` on system units](SYSTEM-UNIT-USER.md) ran on the
disposable Windows 11 Enterprise LTSC acceptance guest. Every step ran as
LocalSystem through the guest agent, with an MSI built from `7823b0a` and new
local standard accounts that never signed in. `7823b0a` still contained a
change to the token's default DACL. That change was removed afterwards because
Windows' own default already admits the account. The LocalSystem tests below
then passed again at `761efa1`.

| Step | Observed |
| --- | --- |
| Start | `member.service` with `User=` a standard account ran `cmd.exe` and its children as that account in session 0. Status showed `User:` with the resolved SID. The unit wrote and read HKCU and a file in its LocalAppData, and its output reached the journal. |
| `unit-of-process` | The main process with `Main=true`, its children with `Main=false`, all with the invocation. |
| Stop | `ExecStop` ran as the account with `MAINPID` set and wrote into the account's LocalAppData. The workload ended and the profile unloaded. |
| Cold boot | Shutdown with the unit running took 6 seconds. After boot, with nobody signed in, the enabled unit ran again as the account 15 seconds after boot. |
| Service process killed | The unit's processes and its profile keeper ended, the profile unloaded, and the SCM restarted the service. |
| Disabled account | The start failed at the S4U logon with Windows' account-restriction message; `Restart=` retried until the start limit. |
| Unknown account | The start failed at the name lookup; `Restart=` retried until the start limit. |
| Removal | `stop`, `disable`, deleting the file and `daemon-reload` left no keeper, and the profile unloaded. |
| LocalSystem tests | At `761efa1`: a process runs as the account in session 0 inside its unit job, with HKCU and profile files; ending the keeper ends the workload; a separate logon of the account terminates another of its processes; two processes keep the profile until the last ends. An administrator account, in an earlier prototype run, got an elevated token. |

The S4U logon is a network logon: its processes belong to `NETWORK`, not to
`INTERACTIVE`, `SERVICE` or `BATCH`. Writing to `C:\Users\Public`, which grants
those groups, was denied, and `taskkill.exe` refused to run. `TerminateProcess`,
for example through PowerShell's `Stop-Process`, worked across logons of the
account.

## Current implementation and remaining identity matrix

| Work package | Delivered behavior and evidence | Remaining qualification |
| --- | --- | --- |
| R4.1 Launch context | Interactive `CreateProcessAsUser` launch disables handle inheritance, obtains the target environment/known folders and retains a Windows-owned profile handle. Suspended creation and job ownership precede execution. Earlier genuine SYSTEM-to-interactive launch/crash/logoff observations are recorded in the [admission contract](USER-ADMISSION.md#implementation-evidence-and-limits). [Selected profile failures and redirection](#interactive-profile-failures-and-redirection) now cover unavailable hive access, local known-folder overrides and delegated UNC/reparse rejection. [Live cross-user and server-validation cases](#live-manager-cross-user-rejection-and-production-client-server-owner-validation) now pass. | Native WTS/S4U file-handle and filtered-token checks now pass as recorded [above](#native-inherited-handles-and-filtered-token-authorization). [Runtime mutation denials and unsafe-root install/repair refusals](#privileged-path-protection-runtime-mutation-attempts-and-unsafe-root-installrepair) now pass for the tested trees and fixtures. The [residual path and handle classes](#residual-path-and-handle-classes) are closed for local accounts. Checked for local and domain accounts, including roaming profiles, by the [domain run](#domain-accounts-and-roaming-profiles), after #297 removed the local-account restriction. Microsoft Entra accounts and folder redirection were not tested; Entra accounts are excluded from supported claims. |
| R4.2 User host | Protected explicit/delegated admission, per-SID overrides, session reconciliation, fresh-token recovery, bounded backoff and cancellation are implemented. [Policy/admission qualification](R2-EVIDENCE.md#user-policy-and-cleanup-admission-acceptance), [real S4U snapshot/recovery](R2-EVIDENCE.md#system-user-host-snapshot-qualification), [two concurrent headless users](#concurrent-local-headless-users), [cross-manager notification isolation](#notification-isolation-across-managers-and-restarts), [control during pending boot](#user-control-during-pending-boot), [repeated real interactive transitions](#repeated-real-interactive-transitions) and [multiple real sessions/concurrent users](#multiple-real-sessions-and-concurrent-interactive-users) cover their stated scopes. Daemon-log startup, rotation and legacy ACL repair are requalified in [the #253 diagnostics evidence](R5-EVIDENCE.md#diagnostics-and-event-resources); recovery-backoff issue #254 was fixed by #271. Dedicated admission administration commands and installer controls remain separate work; protected file administration is available. | Technical acceptance complete with [native launch overlap and consolidated no-resurrection evidence](#native-launch-overlap-and-user-host-acceptance). Domain accounts and roaming profiles are covered under R4.1, credential limits under R4.5. |
| R4.4 Security | Protected policy and pipe checks, bounded impersonation workers and reparse-point rejection are implemented. [Pipe failure handling](R2-EVIDENCE.md#september-12-pipe-impersonation-failure-handling), [genuine cross-user/system pipe denial](#concurrent-local-headless-users) and [native WTS/S4U/filtered-token probes](#native-inherited-handles-and-filtered-token-authorization) cover their stated scopes. [Pipe ownership/DACL/token checks, UAC-filtered users and cross-user rejection](#live-manager-cross-user-rejection-and-production-client-server-owner-validation) are qualified natively with live managers and anti-squatting, within the recorded endpoint scope. [Protected-tree runtime mutation denials and static install/repair refusal of unsafe-root and custom-base fixtures](#privileged-path-protection-runtime-mutation-attempts-and-unsafe-root-installrepair) are qualified for the tested trees and fixtures. | None for local accounts: the [residual path and handle classes](#residual-path-and-handle-classes) are closed, with the data-directory startup check from #295. Domain accounts and roaming profiles are covered under R4.1. |
| R4.5 Linger | Headless local-account S4U launch uses `CreateProcessWithTokenW(LOGON_WITH_PROFILE)` and an exclusive private desktop helper. Windows owns profile lifetime; the old manual `LoadUserProfile` path is removed. [Production crash/recovery](R3-EVIDENCE.md#managed-bound-dependent-cleanup), [user-manager replacements](R2-EVIDENCE.md#system-user-host-snapshot-qualification) and [concurrent existing/first-created profiles](#concurrent-local-headless-users) verify profile/process cleanup in their scenarios. | Checked for local accounts by the [headless linger and credential limits](#headless-linger-and-credential-limits) run. Domain accounts behave the same ([domain run](#domain-accounts-and-roaming-profiles)). The credential-store mode was not exercised and remains outside supported claims. |

With R4.1 checked, the real-identity matrix is complete for local and domain
accounts. Microsoft Entra accounts remain outside it and are excluded from
supported claims.
