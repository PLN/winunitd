# R2 coordinator authority and worker audit

Reviewed source: `4855ce8c90fc2676f608ef778371592fd6a117e5`, September 13, 2026.
Source review found one serialized decision authority in each manager domain.
The worker review found and fixed notification-reader admission in PR #207.
The final 61-case matrix passed three repetitions under both SYSTEM and a
headless standard user, with no skips and complete fixture cleanup.
[Consolidated evidence](R2-EVIDENCE.md#consolidated-coordinator-acceptance) records
exact source, CI, execution scope and the equal-tree merge.
The contract is [Design sections 2-3](../DESIGN.md#2-architecture-and-ownership).

## Review method and authority

The review inventories production assignments, increment/decrement, map deletion,
runtime construction, transition calls and goroutine creation. It follows the
containing functions, their callers, lock boundaries and resource lifetimes.
An AST inventory assists coverage; assignment counts alone are not acceptance.
Configuration candidates and worker-local results are distinguished from accepted
runtime records. Test hooks are observations; they do not authorize publication.

`Manager.mu` serializes unit/configuration/operation decisions. `UserHost.mu`
serializes session, admission, linger, instance and native-token ownership.
System snapshots and the combined shutdown barrier acquire manager then user-host
mutex. User-host decisions never acquire the manager mutex. Unit/SID gates order
effects for one owner; configuration and linger I/O locks serialize those effects.
They do not publish a second lifecycle state.

| Writer set, relative to `internal/manager` | Accepted authority and observation boundary |
| --- | --- |
| `lifecycle_launch.go`, `lifecycle_events.go`, `supervise.go` | Launch/exit/readiness/watchdog decisions retain runtime pointer, generation, captured definition and process identity. Workers return observations; stale completions retain or release their own resources. No transaction result is replayed into unit state. |
| `lifecycle_config.go`, `reload.go`, `enable.go`, `config_work.go` | Parse, filesystem and candidate graph work precede acceptance. Reload revalidates retained ownership and retries at most three invalidated candidates. The admitted configuration effect remains tracked through close. |
| `manager.go`, `shutdown.go`, `operation_lifetime.go`, `lifecycle_operations.go`, `operations.go`, `start_coalescing.go` | Admission, retained member counts, cancellation, stop epochs, completion and history publish under the manager mutex. Accepted operation contexts outlive response waits. Completion removes active ownership and publishes its terminal record in the same decision. |
| `lifecycle_bound.go`, `stop_plan.go` | Dependency-stop membership and ordering use captured invocation definitions. One dispatcher consumes coalesced intents; each member rechecks owner and stop epoch. |
| `lifecycle_watch.go`, `watchhub.go`, `notify.go`, `lifecycle_native.go` | Exact-handle cleanup and exact-invocation observations publish through decisions. Native open/close/query/probe work occurs outside the mutex. Reload cleanup has one admission per retained hub. |
| `lifecycle_health.go`, health/readiness probe workers | Health publication checks the current runtime identity, cancellation, watchdog and retained process under the manager mutex. Probe I/O and subsequent watchdog cleanup run outside it; stale results cannot change replacement health. |
| `lifecycle_timer.go`, `timer.go`, `session.go` | Timer arms carry token/revision identity; callbacks validate storage eligibility and current origin. Session-target synchronization uses admitted public start/stop paths. Scheduler deadlines and session-presence reads are observations. |
| `lifecycle_journal.go`, `stop_helper.go` | Captures and helpers remain attached to their retained invocation until completion. Partial helper launches, output and failed cleanup remain reachable. Helper capture/error results are read only after its completion channel closes. |
| `unitruntime.go`, `cleanup_resources.go`, `snapshot.go` | Transition/resource helpers run within the decision authority. Snapshots copy accepted records and active operations under the same lock, then encode and check response size after unlock. |
| `lifecycle_userhost.go`, `lifecycle_userpolicy.go`, `user_recovery.go` | Instance, session-request, admission-revision and linger-revision decisions validate their captured owner. Token/process/persistence results cannot substitute a newer instance or policy. |
| `user_native_work.go`, `user_idle_cleanup.go`, `user_reconcile_dispatch.go`, `user_shutdown.go` | Native slot ownership, retained token cleanup, idle admission/completion and reconciliation cursor changes use the user-host decision mutex. Workers perform token/stop work outside it; retries join retained native attempts. |

Constructors initialize before publication. `notifyRuntime` message/PID fields
and `watchRuntime.watches` are adapter-owned state, not unit lifecycle writers.
`stopSet` publishes immutable results before closing each attempt's channel.
`closePending` transfers detached controls under the manager mutex; close joins
accepted configuration work before collecting its final teardown set.

The nested manager-to-timer and manager-to-journal admission locks perform bounded
memory work. Scheduler callbacks, calendar searches and persistence run after
unlock; journal queue admission never performs file I/O. Error formatting and
classification happen before lifecycle decisions. Journal storage-error text is
also observed before its admission mutex, and timer load/save error text before
the engine mutex. Locally constructed timer, graph and
cancellation errors contain owned values. Maintenance's retained shutdown error
contains messages already rendered by the system/user shutdown workers.

## Worker admission, retention and completion

These are default per-manager limits unless a row names the system user host.
Transport and operation budgets are independent. Completion does not consume an
ordinary command slot. A timed-out caller does not release a native worker that
still owns an incomplete operation.

| Class | Bound and completion behavior |
| --- | --- |
| Control transport | 128 accepted connections; 64 ordinary, 8 stop and 8 diagnostic handlers. Excess connections close before authentication; authenticated class overload reports busy. Independent maintenance admission remains available through its reserved control path. |
| Notification readers | 64 connections per listener, reserved before PID authorization or spawning. Idle/banner-blocked clients retain slots; denied and excess clients close without acceptance. Cancellation closes connections and joins admitted readers. |
| Explicit start/restart and stop | 32 start/restart and 32 stop transactions by default; each graph transaction has at most 16 workers. Compatible starts share one flight. Slots/retained records survive caller disconnection and release on accepted completion. |
| Operation deadlines/history | One deadline watcher per accepted operation; 256 completed records. A 4096-byte retained error bound prevents a single operation from retaining an arbitrary diagnostic allocation. |
| Dependency stops | One reserved dispatcher and at most one pending intent per retained unit name; captured stop plans retain member ownership through execution. |
| Recovery and failed-process cleanup | Recovery admission precedes worker creation and rejects duplicate current-invocation requests. Backoff and gate waits honor cancellation. Failed-process cleanup marks the retained resource before spawning, preventing duplicate admission. These are tied to the 1024 retained-unit budget. |
| Process wait, readiness and liveness | Observers belong to their retained invocation. Readiness/liveness waits use cancellation and generation identity; a retained native call cannot be replaced merely because its response deadline expired. |
| Native proxy observations | Four query workers; old record/generation/stop-epoch results cannot change replacements. Close joins outstanding observations. |
| Cooperative stop helpers | 32 admitted helper records, each retaining late creation, process and output until confirmed cleanup. Retries join the same helper. |
| Native stop/close calls | One pending `stopSet` attempt per exact process, handle, target or shutdown owner. Caller timeouts do not spawn a duplicate OS call; completed failed attempts may be explicitly retried. |
| Native watch subscriptions | One event loop per accepted specification and one optional initial path activation per hub. Configuration limits are 1024 retained units and 64 KiB per file. Close fanout follows the captured specification set; reload has one cleanup admission per retained hub. Maximum-configuration stress remains R5.5. |
| Timer engine | 1024 arms, 64 calendar expressions per arm, 32 active callbacks, one reserved calendar planner and one coalesced state-loading worker. Pending arms retain capacity-limited occurrences; stop joins callbacks/planning/storage work. |
| Journal | One fair capture writer; aggregate 16 MiB/16,384 queued records and per-group 4 MiB/12,288 records. Four sync and four query workers. Retained captures own stream drain and cleanup; timeout cannot release unfinished sync ownership. |
| User-host native work | Four ordinary admissions plus one each for reconciliation, policy, linger scan and explicit revocation: eight slots total. Failed token close retains its slot; reconciliation or shutdown joins cleanup. |
| User-host records and session requests | 128 manager/linger records; 4096 interactive session and pending-request identities. Late results require the accepted request/revision. |
| User-host idle cleanup | One dispatcher, four workers, at most 132 requests (128 owners plus four retired active completions). Busy SID gates consume no worker; duplicate requests coalesce. |
| User-host shutdown and maintenance | Four user stop workers in one retained shutdown pass; system and user teardown progress independently behind one combined admission barrier. Repeated maintenance waits join the current attempt. |

No bound promises immediate completion of an uncancellable Windows call. Such
work remains owned and visible; it can block replacement or maintenance success.
Admission limits do not imply a whole-machine memory or latency guarantee under
maximum configuration. Aggregate stress, historical journal disk retention and
broader session/security acceptance retain their R4/R5 gates.

## Invariant and sequence matrix

The accepted matrix selected 50 manager, four journal, three timer and four
notification cases, three repetitions per SYSTEM/headless standard-user identity.
Most use deliberately delayed adapters
through public lifecycle/control paths. Native maintenance saturation, restart
backoff and invocation replacement additionally execute real Windows workloads.
The complete exact-source race suite remains the broader regression check.
Timer cases cover blocked load and error observation, write-failure suspension,
repair, stale arms and independent engine decisions.
Notification cases cover portable and actual Windows-pipe saturation, denied
clients, PID authorization, slot recovery, acceptance and cancellation joins.

| Design invariant | Representative selected cases |
| --- | --- |
| 1. Ownership through cleanup and reload | `TestFailedLaunchRetainsReturnedProcess`, `TestLateLaunchCleanupFailureRetainsOwnership`, `TestStartPlanRetainsDefinitionsAcrossReload`, `TestJournalCleanupRetainsRemovedRecordAndClose` |
| 2. One owned invocation | `TestConcurrentStopThenStartLeavesSecondProcess`, `TestStartTransactionDoesNotReviveExitedMember`, `TestLateRecoveryCannotAffectRecreatedUnit`, `TestWindowsRestartAlwaysGetsNewInvocationID` |
| 3. Current identity only | `TestRejectedMemberCannotOverwriteNewInvocation`, `TestLateCallbackCannotAffectAutomaticReplacement`, `TestStopTransactionDoesNotOverwriteLaterFailedStart`, `TestStaleJournalCompletionCannotClearReplacement` |
| 4. Stop/maintenance precedence | `TestCancelRestartDuringStopDoesNotRelaunch`, `TestRecoveryRevalidatesAfterWaitingForUnitGate`, `TestShutdownAllSealsBothDomainsBeforeCleanup`, `TestWindowsMaintenanceSurvivesFullControlConnections` |
| 5. Independent I/O and ordered transitions | `TestRejectedMemberPublishesBeforeIndependentWorkerReturns`, `TestStopMidStartTransactionDoesNotActivateMember`, `TestLaunchErrorObservationPreservesIndependentProgress`, `TestControlAdmissionPreservesSnapshotStopAndMaintenance` |
| 6. Independent output completion | `TestConcurrentCaptureDrainsDuringStorageStall`, `TestStorageErrorObservationPreservesQueueProgress`, `TestDiagnosticsBoundStorageStallAndPreserveIdentity`, `TestRotationFailureRetainsProgressAndCaptureCleanup` |
| 7. Honest snapshots and outcomes | `TestSnapshotKeepsAcceptedOperationAndUnitTogether`, `TestSnapshotCopiesCleanupAndRetainsOwnedPID`, `TestSnapshotRetainsTerminalFailureDiagnostics`, `TestLateFailedStopKeepsOutcomeAfterSuccessfulRetry` |

Additional selected cases cover canceled response waits, accepted stops,
completed dependencies, queued timer/watch admission, policy recovery, retired-SID
churn, reload cleanup, snapshot bounds and concurrent lifecycle membership.
Individual fix evidence is linked from [R2 evidence](R2-EVIDENCE.md).
