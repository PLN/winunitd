# R1 implementation evidence

This record supports [milestone R1](MILESTONES.md#r1--ownership-output-and-failure-containment). It records implementation progress, not installation qualification or milestone acceptance. All test paths and fixtures use isolated resources.

## Ownership and cleanup behavior

Explicit stop, failed-state reaping, ordinary exit, replacement of an exited invocation, readiness/watchdog failure, and late launch disposal retain process ownership until cleanup succeeds. Failures remain available for stop retry and block replacement/restart. Retries join a pending adapter call after caller timeout. Windows unit and user-manager adapters close job admission, capture process handles, and wait for those handles plus an empty job list before releasing ownership. Kill/wait/query/close failures retain unfinished handles for retry.

Shutdown closes start admission before its stop snapshot and includes accepted launches even before they publish a process. Shutdown context deadlines now bound operation-lock, pending process-stop, and journal waits; expiration retains pending ownership and permits shutdown retry. SCM/task stop retries also join a pending native call after deadline instead of spawning another blocked call.

Per-user host starts/stops are serialized per SID; failed logoff/shutdown cleanup and failed launches remain tracked, and shutdown deadlines join pending kills on retry. Session requests are registered before token lookup; logoff or session-ID reuse invalidates stale results, and late launches are cleaned up. Linger launches recheck policy after token lookup.

Daemon-job assignment verifies membership explicitly; access-denied errors no longer imply ownership, and both launchers assign through the original process handle before resuming the child, with the outer job assigned first so unit/user jobs remain siblings. Daemon-job close now seals admission, verifies membership before individual child termination, confirms captured exits, and retains failed cleanup instead of discarding the job handle. It clears kill-on-close only after descendants are confirmed gone.

Manager close now accepts a caller deadline, joins a pending cleanup pass, returns journal/watch close errors, and retains failed watch teardown for retry. System/user daemon cleanup now shares the existing SCM stop window across user-host shutdown, unit shutdown, manager close, and daemon-job close. Pending shutdown/job-close calls are joined on retry, and joined cancellation cannot mask a cleanup failure in console or SCM exit results. Failure to assign the daemon itself to its job now prevents startup.

Native watcher close retries retain failed handles and drain outstanding waits/reads. Manager stop and reload retain watch ownership, reject replacement during unresolved cleanup, and join pending closes across caller deadlines. Partial opens retain their cleanup on the unit when possible, otherwise on the manager. Real Windows session qualification and remaining asynchronous teardown paths still need qualification. Stop remains forced Job Object termination until R3.

## Validation

All GitHub CI lanes passed for `2371a54`. Full local race tests with CI flags and vet cover unit/native cleanup, real-child storage stalls, and failed launch ownership. Focused repetitions include 100 timer activations, ten shutdown/native-stop deadline scenarios, and five real-child storage stalls. The timer/readiness regressions distinguish their fake-clock deadlines from cleanup deadlines.

An early-child-exit regression exposed that an empty job list (and zero active-process count) can precede process-handle signaling; exit capture now closes admission before enumeration and retains synchronization handles across retries. Twenty focused Windows repetitions and protected-handle tests verify termination confirmation, close failures, and retry.

Per-user host regressions cover concurrent starts, failed logoff, late launch during shutdown, partial launch failure, and repeated deadlines joining one pending kill; ten focused repetitions pass. Windows per-user tests also verify retained failed-launch cleanup and termination of a process outside its retained job. Twenty repeated session-race tests cover delayed token lookup, session-ID reuse, logoff during launch, and disabling linger during token lookup.

Ten Windows repetitions verify that denied daemon-job assignment fails both launchers, cleans up their children, and that stopping one user manager leaves its sibling alive. Linux CI exposed an instantaneous-lock assertion that conflicted with legitimate late-exit cleanup; it now checks bounded acquisition and passes 100 focused repetitions.

Isolated self-contained daemon tests pass five repetitions each for successful close and query/termination/captured-handle/job-handle failures followed by retry.

Ten portable repetitions verify close deadlines joining one blocked watcher and successful retry after a reported watcher-close failure. Ten repetitions also cover blocked notification shutdown, fresh-context retry, bounded daemon-job close, aggregate shutdown deadline/ownership retention, and SCM reporting of cleanup failures joined with cancellation.

Five journal repetitions cover partial disk-full/short-write failures, preservation of the next record after reopening, a complete final record missing its newline, and noisy/quiet invocation admission.

Native registry, Event Log, and directory watchers now serialize close retries and retain failed handles. Registry close drains its waiter; directory close waits for canceled overlapped reads before releasing buffers and handles. PathExists retains failed replaced watches and watches opened during close. Protected-handle, concurrent-close, callback-failure, and replacement-ownership regressions pass, along with the full local race suite and vet.

Twenty repeated manager tests cover failed watch stop retained across reload, pending-close deadlines, and partial-open failure blocking replacement. A retry can join an already pending failure; a subsequent fresh attempt is tested separately. CI confirmation for the manager watcher changes remains pending. These results are implementation progress, not milestone closure or installation qualification.

## Remaining qualification

- Notification listener cleanup must propagate failures and retain ownership across every teardown path.
- Post-allocation failures inside watcher open adapters need an explicit ownership contract.
- Windows identity/session scenarios and broader installer qualification remain open.
- Journal qualification still needs real-volume exhaustion, buffered-writer recovery without reopening, aggregate overload fairness, read-path stalls, and lifecycle admission bounds.
- Immutable configuration revisions and stale-event handling remain R2 work; service stop semantics remain R3 work.
