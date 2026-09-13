# Unit semantics qualification

September 13, 2026: R3 technical acceptance is complete. The native conformance
packages below cover its work items, and [consolidated R2 acceptance](R2-EVIDENCE.md#consolidated-coordinator-acceptance)
satisfies its coordinator dependency. Earlier open-dependency wording records
the state at each individual qualification. Broader Windows identity/security,
operational stress, installer and pilot gates remain separate in
[milestones](MILESTONES.md).

## Native proxy ownership diagnostics

PR #190 source `71c27e917ba949a2279e1719b53049e7934841e8` passed
[exact-source CI](https://github.com/PLN/winunitd/actions/runs/34761515360).
Disposable LTSC build 26100 SYSTEM qualification passed three repetitions each
of the native SCM/task disappearance fixture and public retarget/type-change
regressions. Eighteen real native start/external-stop/dependent-cleanup cycles
verified captured target, Windows owner and request capabilities through status,
list and immutable snapshot. The complete matrix took 21.847 seconds with no
skips; fixture cleanup passed and the hosting broker remained running. Merge
`1d07f60bb63ebc514aac7fe8048dd0e8b173373b` has the tested tree.

Caller mutation of one view cannot change another view or the next capture.
Reload retains old proxy metadata until cleanup/replacement, including changes
between managed processes, SCM services and tasks. Diagnostics explicitly report
no process-tree ownership, output capture, ExecStop/job-limit support or native
definition management. Capabilities describe the adapter contract, not queried
access rights or guaranteed request success. Proxies remain system-manager only.
Twenty focused local race repetitions, the full uncached race suite, vet and
Windows/Linux staticcheck passed. Raw artifacts, module hashes, CI and native
logs are retained privately.

Together with the trigger/CPU matrix below, this completes technical R3.5.
All technical R3 packages are delivered; overall milestone closure still requires
R2 acceptance and preservation of the combined conformance invariants.

## Dependency and native trigger conformance

PR #187 source `a2ed9b4883d20ea72f5912c14810f29c70599d12` passed
[exact-source CI](https://github.com/PLN/winunitd/actions/runs/34760487473)
and the consolidated disposable LTSC build 26100 matrix. All 27 SYSTEM cases
and 17 headless standard-user cases passed three repetitions without skips,
in 57.437 and 22.218 seconds respectively. Merge
`f7d0ff1e80f09e45c8ee5d25f6d2bdf6ce139e41` has the tested tree.

The native cases cover PartOf restart without root Wants, three successive
process replacements, captured membership after reload, and adoption of new
membership on the next invocation. Unexpected peer exit stops BindsTo members
while Requires/PartOf peers remain running; explicit stop cleans their captured
scope. Public-path regressions additionally verify stop/restart/shutdown ordering
across changed definitions and preservation of dependents for paused/pending or
missing SCM observations. Earlier real SCM/task inactivity qualification is
recorded below. The reference distinguishes requirement propagation, ordering,
partial transaction results and native uncertainty.

The same native matrix covers format-2 CPU controls and OR path policy; changed,
missing, disabled, file-filtered and initial/later existence watches; registry
creation/deletion/disable and running-service behavior; and event-channel/event-ID
filtering, missing channels, disable and running-service behavior. SYSTEM rejects
HKCU watches; the headless user passes real loaded-HKCU notification and rejects
System event-log access. Trigger events remain coalescible notifications.

An initial matrix found two fixture errors, retained with its failed evidence:
a directory watch legitimately reported immediate-child directory metadata, and
the HKCU helper incorrectly equated session 0 with a service identity. The
corrected file-specific negative/positive test and token/loaded-hive checks passed
twenty local race repetitions before the final native matrix. Production path
filtering was unchanged. All fixture processes exited, linger was disabled,
the user profile unloaded, and the original hosting broker remained running.

This completes technical R3.2. The later proxy-diagnostic qualification above
completes technical R3.5; overall R3 depends on R2 acceptance.

## Explicit format-2 policies and migration

The versioned [unit reference](UNIT-REFERENCE.md#format-selection-and-migration)
specifies the default legacy boundary, format-2 OR/explicit-AND path predicates,
native Windows CPU names and ranges, and literal executable argument rules.
`winctl migrate` produces a validated preview or exclusively creates a new file;
it preserves effective legacy path and CPU policy without rewriting installed
units. Unsupported versions and ambiguous combinations are rejected.

Required regressions cover conversion, invalid/bounded input and output,
idempotence, exclusive destination creation, and legacy/new parser behavior.
Ten local Windows race repetitions query actual process Job Object weights
1/5/9 and hard caps 25%/100%, inspect status, and confirm process cleanup.
The native filesystem regression verifies initial OR activation, a later rising
edge, and retention of the armed policy after reloading an AND replacement.
At source `ad5f17fbac322e19b5c2923c263c77c54d54e885`,
[exact-source hosted CI](https://github.com/PLN/winunitd/actions/runs/34753463620)
passed. Disposable Windows LTSC build 26100 qualification passed both native
test groups twenty times as SYSTEM and twenty times as a headless standard user,
with no skips. All fixture processes exited; disabling linger released the user
profile and left no user manager or desktop helper. PR #175 merged the same
tested tree as `d46d84a938720bb860cb401ad8dda25bdcb2f01c`. This qualifies the
format/path/CPU slice; overall R3 acceptance remains open.

## Native proxy inactivity observation

Background SCM/task observations now deliver exact record/generation/stop-epoch
results through one coordinator handler. Four retained query slots and rotating
admission preserve bounded work; close joins blocked native calls. Only confirmed
stopped/idle states stop BindsTo dependents. Query errors remain unknown and have
bounded diagnostics; fresh native status remains an independent observation.

Regressions cover captured native targets and dependent policies after reload,
delayed results after replacement, blocked-query capacity and close retries,
progress beside a blocked query, error recovery and task-instance disambiguation.
The local Windows task fixture passed three real external-stop/dependent-cleanup
cycles at about one second each. Local SCM fixture creation was skipped for lack
of service-registration access. Subsequent disposable LTSC SYSTEM qualification
passed all six cases at `14cfc987a7c3d79b85607be94906068129f4cb8f`: three SCM
and three scheduled-task cycles, including real dependent process cleanup, in
0.998-1.012 seconds per cycle. No cases were skipped; no fixture services, tasks
or processes remained. [Exact-source CI](https://github.com/PLN/winunitd/actions/runs/34750966320)
passed, and PR #170 merged the same tested tree. An initial task registration
fixture failed due to XML encoding; the corrected UTF-16 fixture and both raw
attempts are retained privately. This does not close missing-target/paused-state
policy or the complete R3 acceptance.

## Unrelated dependency-event allocation

Bound-stop decisions discover active dependents before collecting graph
definitions. With 1024 loaded/retained units whose captured policies bind only
to other peers, the regression reproduced nine allocations per unrelated exit
before this change and now reports zero bytes/allocations. The local benchmark
measured 43.267 microseconds per decision. Twenty race repetitions of the bound
tests and the full uncached Windows race suite/vet passed. Positive plans still
use captured invocation definitions, including retained reload policy, and build
their graph under the decision lock; a reverse dependency index remains a separate
optimization. This does not change the earlier native dependency evidence scope.

## Tracked cooperative stop

September 12, 2026: PR #147 implements one `ExecStop` command with independent
helper ownership, captured invocation context and a forced-cleanup reserve.
The qualified source is `b8f356ed72efb65ed9a4b80b300ab7c782563c22`;
[exact-source CI](https://github.com/PLN/winunitd/actions/runs/34718242627) passed
Windows/Linux tests, vulnerability checks and the Windows cross-build. Merge
`9553fc444db849f0a6641fae0c0fed5a7ea4185e` has the same tested tree.

The disposable guest used the downloaded Windows artifact after manifest, source,
dependency and installed-payload hash verification. Eight real-process cases
passed through an SCM-owned SYSTEM manager and a production headless S4U user
manager:

| Case | SYSTEM | Headless user |
| --- | --- | --- |
| Cooperative helper and clean workload exit | 1.092s | 1.405s |
| Hung helper: force workload, helper and helper child | 8.037s | 8.034s |
| Repeatable oneshot: helper during completion | Passed | Passed |
| Retained oneshot: helper only on explicit stop | Passed | Passed |

The forced cases configured a ten-second stop budget: force followed the
eight-second cooperative allowance, returned failed rather than successful
cooperation, and confirmed cleanup. The checks also verified helper/workload
token identity, working directory, configured environment, `MAINPID` absence for
an exited oneshot, linked invocation identity, helper journal output, and no
re-execution on stop retry. All fixture definitions were removed. Disabling
linger stopped the user manager and desktop helper, released its profile, and
prevented resurrection; the SYSTEM broker remained running.

Required manager regressions in `stop_helper_test.go` additionally cover captured
configuration across reload, restart across successive invocations, failed-start
exclusion, late creation after the caller deadline, failed helper cleanup,
unfinished output retention, parent-budget reservation, the 32-helper admission
limit, and Close joining accepted work. Focused cases passed twenty race-test
repetitions; the full uncached Windows race suite and vet passed.

This closes the single-command R3.3b slice. It does not close the remaining unit
format, dependency, health or multi-session qualification gates, nor qualify MSI
servicing or the real application soak. Multiple stop commands, automatic
console/GUI signals and stop helpers for external proxies remain unsupported.
Raw scripts, process observations and artifact evidence remain in the private
qualification store; private identities and paths are intentionally not copied
into this repository.

## Managed bound-dependent cleanup

September 13: managed peer exit/failure now admits captured BindsTo cleanup,
including reverse stop ordering and recovery suppression before teardown.
One coalescing worker reserves progress outside client stop admission and retains
accepted work across caller/close deadlines. Delayed record/generation/epoch
checks preserve replacement processes and their operation metadata. BindsTo plus
After requires an active peer, including the explicit retained-oneshot case.

Twenty focused race repetitions cover peer recovery without dependent restart,
Requires/PartOf distinction, removed-definition policy, ordered scope suppression,
client stop saturation, stale peer/watchdog/member events, delayed batch metadata,
dependent activation during blocked peer cleanup, retained-peer recovery/start-limit,
and close both before worker dispatch and during blocked cleanup. The full
uncached Windows race suite and vet pass. These are manager regression results;
native artifact qualification and external SCM/task disappearance observation
remain separate. This does not close the full R3.2/R3 milestone.

The immutable Windows artifact at `e0a9ab78466096ea50f96365697201856e3676dd`
subsequently passed ten native dependency cases on the disposable Enterprise LTSC
baseline, split equally between genuine SYSTEM and a headless standard user.
[Exact-source CI](https://github.com/PLN/winunitd/actions/runs/34725134485)
passed before qualification and PR #157 merged the identical runtime tree.
Peer exit stopped the bound dependent while the peer recovered and a Requires-only
dependent stayed active. Reload preserved the running invocation's old binding;
a replacement invocation used the new unbound policy. Both scopes also verified
that repeatable oneshot completion stops its dependent, while retained completion
keeps it active. The peer recovery/observation cases completed in 1.055–1.088 seconds.

The same candidate passed eight cooperative/forced/oneshot stop cases, accepted
user snapshot observation, and pending timer recovery after broker crash in
2.689 seconds with the same activation identity and no replay on a second restart.
Final disable-linger released the manager, helper and user profile and prevented
resurrection. Raw scripts, results, artifact hashes, manifest and CI identity are
retained privately. External SCM/task disappearance was subsequently qualified
in the native proxy section above. The remaining full R3 acceptance matrix stays
open; these results do not qualify MSI servicing or R7.

## Capped restart backoff qualification

PR #181 source `6f75e0d4078f0fa9468f0e904fc1538928cda8a7` passed
[exact-source CI](https://github.com/PLN/winunitd/actions/runs/34757140475)
and merged with an identical tree. Format-2 exponential backoff has a required
finite cap, retains invocation policy through reload, resets on a real explicit
launch and keeps timer/watch start limits intact. Duplicate generation recovery
cannot replace an accepted wait. Status and snapshots copy the bounded step and
delay; stop/removal/shutdown disarm recovery.

Twenty native repetitions as SYSTEM and twenty as a headless standard user on
Windows 11 Enterprise LTSC build 26100 verified four actual exit-7 launches,
the combined minimum backoff delay, start-limit exhaustion and consistent terminal
snapshot/status identity. No selected test skipped. Final teardown removed the
fixture and user manager/helper, disabled linger and unloaded the profile; the
hosting broker and real pilot were unchanged. Raw logs, artifact/module hashes,
CI and cleanup results remain privately retained. Ten focused race repetitions,
the full local race suite, vet and Windows/Linux staticcheck also passed.
This delivers recovery backoff. The combined readiness/health acceptance below
subsequently qualified it alongside the new startup and liveness policies.

## Startup readiness and health acceptance

PR #182 source `16b781f2481b20c996bb161326e83d17a5cb0f28` passed
[exact-source CI](https://github.com/PLN/winunitd/actions/runs/34758052219)
and twenty native health-sequence repetitions per SYSTEM/headless-user identity.
Grace, per-probe deadlines and consecutive-failure thresholds preserve a live
process across transient failures. Successful probes reset the count; threshold
exhaustion uses existing owned cleanup and recovery. HTTP body errors and
deadlines cannot report success. Reload and stale-result tests retain policy.

PR #183 source `c93341bbd180a1c5ef30aac49424a78d19de9e6e` adds separate loopback
HTTP/TCP startup probes for format-2 simple services. Activation and dependent
ordering wait for success within the shared creation/readiness deadline. Stop,
process exit and timeout retain truthful cleanup; watchdog starts after activation.
Endpoint responses do not establish listener ownership, authentication or version.
The [configuration contract](UNIT-REFERENCE.md#startup-readiness-probes) records
the defaults, supported combinations and limits.

[Exact-source CI](https://github.com/PLN/winunitd/actions/runs/34758620683)
passed. The combined matrix below passed on Windows 11 Enterprise LTSC build
26100 as genuine SYSTEM and as a headless nonadministrator user. Every row ran
ten times per identity; no selected test skipped.

| Native case | Behavior checked | SYSTEM / user repetitions |
| --- | --- | --- |
| `TestWindowsFormat2NativeCPUControls` | Actual Job Object weights and hard caps | 10 / 10 |
| `TestWindowsFormat2PathORRetainsArmedPolicy` | OR activation, rising edge and captured reload policy | 10 / 10 |
| `TestWindowsRestartBackoffRespectsNativeFailureLimit` | Four exit-7 launches, capped delay and start limit | 10 / 10 |
| `TestWindowsProbeHealthRetainsTransientFailure` | 503/200/503/503 health, live process before threshold and cleanup | 10 / 10 |
| `TestWindowsHTTPReadinessActivationAndCleanup` | Success, aggregate timeout, explicit stop and process cleanup | 10 / 10 |
| `TestWindowsTCPReadinessAcceptsLoopbackConnection` | Accepted TCP readiness and process cleanup | 10 / 10 |

The matrix took 14.287 seconds as SYSTEM and 13.147 seconds as the headless user.
Final teardown removed the fixture, disabled linger, released the user manager
and helper, and unloaded its profile; the hosting broker stayed running. Both PRs
merged with their exact tested trees. Artifact/module hashes, CI identity, native
logs, pass counts and cleanup observations remain privately retained.

Ten focused local race repetitions, the full uncached race suite, vet and both
platform staticchecks passed. Portable regressions cover dependent ordering,
stop/deadline/process-exit races, captured reload policy, stale observations,
hung HTTP bodies and duplicate recovery acceptance. This delivers the R3.4
technical slice. Overall R3 still depends on R2 and remaining dependency/native
acceptance. MSI handoff, broader Windows/session support and R7 remain separate.
