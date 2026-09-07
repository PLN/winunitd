# Lifecycle writer audit

September 7, 2026. Working inventory for [issue #96](https://github.com/PLN/winunitd/issues/96),
against [Design v2 sections 2-3](../DESIGN.md#3-records-and-invariants).
This is an incremental migration map, not evidence that R2 is complete.

## Current authority and migration boundary

The manager mutex serializes record access, but callers and workers still make
lifecycle decisions in several files. A mutex is not the design's coordinator.
The per-unit gate serializes native work for one name while independent units
perform I/O concurrently. Typed start, stop, watchdog and exit handlers are migration
boundaries; they currently execute on the delivering goroutine under that mutex.

Graph execution now reports never-launched failures through StartRejectionObserver.
The manager delivers those failures using the accepted runtime record, generation,
stop epoch and origin. Successful/failed adapter calls publish their member outcome
while still holding the unit gate. A canceled gate wait also publishes a rejection.
Each real automatic or explicit launch receives a fresh generation; redundant
starts retain the live invocation identity. No final transaction Run is applied to manager lifecycle state. Run remains a graph
execution result; a failed operation does not rewrite successfully started members.

## Writer inventory

Paths below are relative to internal/manager. Include ownership and eligibility
writes as well as state/substate assignment when migrating each row.

| Source and entry points | Decisions and owned fields | Remaining migration |
| --- | --- | --- |
| manager.go: startOperation, CloseContext; operations.go; start_coalescing.go | Admission counters, retained operation records, captured plans, shared waiters, history, manager close barrier | Coordinator command admission and completion publication |
| operation_lifetime.go: beginOperationTaskLocked, cancelOperationLocked, finishOperationTask | Accepted context, deadline, stop epoch, recovery suppression, uncertainty, slot release | Coordinator-owned operation records; timer callbacks submit cancellation events |
| lifecycle_events.go: start/rejection, stop cleanup/completion, process exit handlers | Member state/error, matching generation, cleanup ownership, restart decision | Typed boundary exists; route through the coordinator after remaining workers stop writing records |
| supervise.go: launchUnitOwnedOp | Invocation generation/configuration, process adoption, notify ownership, readiness/oneshot results, start cancellation, late cleanup | Split admission, native observations and cleanup completion into typed decisions |
| supervise.go: watch, reapFailedLocked, reapFailed | Exit cleanup ownership, uncertainty, process removal, failure diagnostics | watch now uses typed cleanup admission/results in lifecycle_events.go; failed-process reaping also uses captured effects and typed results |
| supervise.go: maybeRestart, beginRestart; startlimit.go | Recovery eligibility, start history/budget, restart cancellation, auto-restart state | Recovery acceptance now uses a lifecycle handler; delay and launch remain workers. Start-limit accounting at launch still needs migration |
| shutdown.go: Shutdown, stopTransaction, stopUnitWithContext, stopUnitAfterLock | Scope suppression, stop epochs/generations, retained records, stop request state, watchdog detach | Complete stop/restart scope is now disarmed at admission through a lifecycle helper; per-member teardown now receives a retained stop effect. Typed stop results already exist |
| notify.go: closeNotifyContext, disposeNotify, startWatchdog, stopWatchdog, onWatchdogTimeout | Notify/watchdog handles, termination intent, watchdog failure, cleanup result | Watchdog timeout now uses typed decision/cleanup events; close results now use typed handlers; watchdog registration/detach now use handlers; handle admission/disposal remain direct writers. notifyRuntime's message channels remain adapter observations |
| scm.go: startSCM; task.go: startTask | Native recovery start success; timer activation notification | Native completions now validate captured owner and cancellation before publishing timer activation; external status queries remain observations |
| reload.go: replaceLocked, acceptConfigRevisionLocked; enable.go: rebuildGraphWithLinksLocked | Accepted graph/configuration, stable record creation/removal, load state, enablement and revisions | Commit validated candidates through coordinator; parsing/persistence outside it |
| watchhub.go: installHub, failHub, disarmHubContext, closeHub, disposeHub, syncHubsLocked | Watch ownership, failure, uncertainty, configuration reconciliation | Close results use typed handlers; arm/fail/disposal decisions remain to migrate |
| pathwatch.go, registry.go, eventlog.go | Origin validation and trigger dispatch; path predicate latch on current hub | Submit origin-bearing activation; keep probes and watch I/O outside coordinator |
| timer.go; internal/timers engine | Armed timer definitions and persistence state, activation origins, reload/stop reconciliation | Coordinate arm/disarm/dispatch; scheduler timing and persistence stay outside lifecycle authority |
| unitruntime.go: step, cancelRestart, detachAsync, error helpers | State/substate transition primitive, cancellation and handle transfer | Restrict calls to coordinator decision handlers; helpers are not separate authorities |
| userhost.go; admission.go; linger.go | Separate UserHost mutex, session/admission/linger policy, bySID instances, launch placeholders, uncertainty and cleanup | Per-SID records/events must join system-manager coordination; keep tokens, process creation and persistence outside it |

Status assembly in manager.go copies records and then overlays timer/native
observations. It does not publish immutable aggregate coordinator snapshots yet.
Native query results can describe a different observation time from the copied
manager fields; retaining the existing response format does not close R2.1.

## Invariants and regression evidence

All test paths below are relative to internal/manager unless noted. These tests
protect existing behavior during migration; they do not substitute for the
remaining SYSTEM/session and VM qualification.

| Design invariant | Existing regression evidence |
| --- | --- |
| 1. Ownership through confirmed cleanup and reload | reload_ownership_test.go; reload_native_test.go; TestLateLaunchCleanupFailureRetainsOwnership in stop_failure_test.go |
| 2. One owned invocation per unit | serialize_test.go; stop_pending_test.go; recovery_identity_test.go; invocation_test.go |
| 3. Only current identity changes lifecycle | plan_config_test.go; stop_completion_test.go; start_rejection_test.go; recovery_identity_test.go |
| 4. Stop/maintenance disarms recovery and late activation | shutdown_test.go; reload_trigger_test.go; timer_revision_test.go; operation_lifetime_test.go |
| 5. Concurrent independent I/O, ordered transitions | internal/core/transaction_test.go and stop_test.go; TestRejectedMemberPublishesBeforeIndependentWorkerReturns in start_rejection_test.go; shutdown_test.go |
| 6. Output drain independent of activation | TestReviewReproOneshotDrainsOutput in review_repro_windows_test.go; shutdown_test.go; internal/journal tests |
| 7. Honest lifecycle, operation outcome and uncertain stop diagnostics | stop_failure_test.go; stop_completion_test.go; operations_test.go; operation_lifetime_test.go; immutable aggregate status remains open |

## Audit procedure and completion gate

Search production Go files for assignments to runtime state, substate, generation,
stop epoch, process/notify/watch handles, invocation/configuration pointers,
uncertainty, cancellation and restart history. Also inspect step calls, runtime
construction, map replacement/deletion, accepted operations, timer engine updates
and UserHost instance/session updates. Review the containing functions and their
lock/I/O boundaries; text search alone cannot establish authority or ownership.

Repeat this inventory after each migration. R2 closes only after every row has one
coordinator decision path, worker observations cannot mutate records, bounded
admission preserves completion delivery, and aggregate status snapshots are
published by that authority. Removing transaction-result application is one
completed slice, not the coordinator itself.
