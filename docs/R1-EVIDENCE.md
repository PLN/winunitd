# R1 implementation evidence

This record supports [milestone R1](MILESTONES.md#r1--ownership-output-and-failure-containment). It records implementation progress, not installation qualification or milestone acceptance. All test paths and fixtures use isolated resources.

## Ownership and cleanup behavior

Explicit stop, failed-state reaping, ordinary exit, replacement of an exited invocation, readiness/watchdog failure, and late launch disposal retain process ownership until cleanup succeeds. Failures remain available for stop retry and block replacement/restart. Retries join a pending adapter call after caller timeout. Windows unit and user-manager adapters close job admission, capture process handles, and wait for those handles plus an empty job list before releasing ownership. Kill/wait/query/close failures retain unfinished handles for retry.

Shutdown closes start admission before its stop snapshot and includes accepted launches even before they publish a process. Shutdown context deadlines now bound operation-lock, pending process-stop, and journal waits; expiration retains pending ownership and permits shutdown retry. SCM/task stop retries also join a pending native call after deadline instead of spawning another blocked call.

Per-user host starts/stops are serialized per SID; failed logoff/shutdown cleanup and failed launches remain tracked, and shutdown deadlines join pending kills on retry. Session requests are registered before token lookup; logoff or session-ID reuse invalidates stale results, and late launches are cleaned up. Linger launches recheck policy after token lookup.

Daemon-job assignment verifies membership explicitly; access-denied errors no longer imply ownership, and both launchers assign through the original process handle before resuming the child, with the outer job assigned first so unit/user jobs remain siblings. Daemon-job close now seals admission, verifies membership before individual child termination, confirms captured exits, and retains failed cleanup instead of discarding the job handle. It clears kill-on-close only after descendants are confirmed gone.

Cross-session user managers are an explicit exception to outer-job membership:
Windows prohibits mixing sessions within a job. The SYSTEM broker allows explicit
breakaway and atomically places these children in separate noninherited,
kill-on-close jobs at creation. Isolated native tests verify placement, broker
crash cleanup, unchanged same-session nesting and denied workload breakaway.
Dedicated jobs remain tracked by UserHost through ordered shutdown and cleanup
retries. Genuine SYSTEM/session transitions require separate VM evidence.

Manager close now accepts a caller deadline, joins a pending cleanup pass, returns journal/watch close errors, and retains failed watch teardown for retry. System/user daemon cleanup now shares the existing SCM stop window across user-host shutdown, unit shutdown, manager close, and daemon-job close. Pending shutdown/job-close calls are joined on retry, and joined cancellation cannot mask a cleanup failure in console or SCM exit results. Failure to assign the daemon itself to its job now prevents startup.

Native watcher close retries retain failed handles and drain outstanding waits/reads. Manager stop and reload retain watch ownership, reject replacement during unresolved cleanup, and join pending closes across caller deadlines. Partial opens retain their cleanup on the unit when possible, otherwise on the manager. Real Windows session qualification and remaining asynchronous teardown paths still need qualification. Stop remains forced Job Object termination until R3.

Notification listeners also retain accepted clients until each connection closes successfully. Close seals accept admission, drains in-flight accepts after native listener close, and includes late clients in cleanup. Failed connection closes remain available to manager stop retry and block replacement. Successful native closes are not repeated, and an already-closed error cannot hide another failure in a joined error. Twenty race-enabled repetitions cover client-close failure, manager ownership/replacement rejection, late acceptance during close, and joined-error handling; the full local race suite and vet pass.

A notification adapter returning both a listener and an open error transfers cleanup ownership to the manager. No process is launched from that partial result. Failed cleanup remains attached to the unit for stop retry, or to manager shutdown if the unit cannot own it. Successful cleanup permits a later start attempt. Both retry paths pass twenty race-enabled repetitions, alongside the full suite and vet.

Path, existence, registry, and event-log adapters use the same resource-plus-error ownership contract. The manager includes the failed opening's resource alongside earlier watches in retained cleanup. Native directory/event allocation and registry notification-arm failures return unfinished handles if cleanup fails. Existence-watch rearming preserves partial opens as pending cleanup and stops rearming. Twenty portable race repetitions cover all four manager adapters; twenty Windows repetitions use protected native handles to verify failed-open cleanup retention and successful retry.

## Validation

All GitHub CI lanes passed for `40ce624`. Full local race tests with CI flags and vet cover unit/native cleanup, real-child storage stalls, and failed launch ownership. Focused repetitions include 100 timer activations, ten shutdown/native-stop deadline scenarios, and five real-child storage stalls. The timer/readiness regressions distinguish their fake-clock deadlines from cleanup deadlines.

An early-child-exit regression exposed that an empty job list (and zero active-process count) can precede process-handle signaling; exit capture now closes admission before enumeration and retains synchronization handles across retries. Twenty focused Windows repetitions and protected-handle tests verify termination confirmation, close failures, and retry.

Per-user host regressions cover concurrent starts, failed logoff, late launch during shutdown, partial launch failure, and repeated deadlines joining one pending kill; ten focused repetitions pass. Windows per-user tests also verify retained failed-launch cleanup and termination of a process outside its retained job. Twenty repeated session-race tests cover delayed token lookup, session-ID reuse, logoff during launch, and disabling linger during token lookup.

Ten Windows repetitions verify that denied daemon-job assignment fails both launchers, cleans up their children, and that stopping one user manager leaves its sibling alive. Linux CI exposed an instantaneous-lock assertion that conflicted with legitimate late-exit cleanup; it now checks bounded acquisition and passes 100 focused repetitions.

Isolated self-contained daemon tests pass five repetitions each for successful close and query/termination/captured-handle/job-handle failures followed by retry.

Ten portable repetitions verify close deadlines joining one blocked watcher and successful retry after a reported watcher-close failure. Ten repetitions also cover blocked notification shutdown, fresh-context retry, bounded daemon-job close, aggregate shutdown deadline/ownership retention, and SCM reporting of cleanup failures joined with cancellation.

Five journal repetitions cover partial disk-full/short-write failures, preservation of the next record after reopening, a complete final record missing its newline, and noisy/quiet invocation admission.

Native registry, Event Log, and directory watchers now serialize close retries and retain failed handles. Registry close drains its waiter; directory close waits for canceled overlapped reads before releasing buffers and handles. PathExists retains failed replaced watches and watches opened during close. Protected-handle, concurrent-close, callback-failure, and replacement-ownership regressions pass, along with the full local race suite and vet.

Twenty repeated manager tests cover failed watch stop retained across reload, pending-close deadlines, and partial-open failure blocking replacement. A retry can join an already pending failure; a subsequent fresh attempt is tested separately. These results are implementation progress, not milestone closure or installation qualification.

Notification listeners now serialize cancellation and close, propagate failures through explicit stop, launch/readiness cleanup, ordinary exit, watchdog failure, and manager close, and retain failed ownership for retry. Ten repeated tests cover these failures, concurrent close waiting for server exit, and deadlines joining the pending listener close. Full local race tests and vet pass. All CI lanes passed for this change in `40ce624`.

## Offline LTSC SYSTEM qualification

A disposable full copy of the maintained Windows 11 Enterprise LTSC Evaluation baseline, build **26100.9168**, passed the service smoke and [runtime regression script](../tools/lab/assets/runtime-checks.ps1) as **SYSTEM**, with Secure Boot enabled and no default route. The retained source baseline was unchanged. This was a baseline copy, not a fresh installation or a generalized template.

The guest consumed the exact native Windows artifact from GitHub run `33975217055`, source `40ce62439e9b75cdb040c9c693493bae75d10955`. All three binary hashes matched the Linux cross-build manifest. The runtime script SHA256 was `7faf7efe78ee0b73e954492492e8a4f2cd5cef9461dbb607c486594983d6f153`; private results record both identities.

Passed scenarios:

- CLI service installation, enable/start, status/logs, explicit stop, and second start.
- Automatic recovery after reboot with a new invocation and exactly one SYSTEM fixture process; SCM stop left no fixture process.
- Oneshot capture of all 5,000 stdout and 5,000 stderr records, plus 131,073 unterminated output bytes across journal fragments.
- Deletion of a live unit definition with retained status, logs, invocation, and stop routing; recreation launched a new invocation.
- Ten notify-ready/start/stop/reopen cycles, followed by SCM cleanup with no surviving runtime fixture.

The initial smoke exposed a harness assumption: its PowerShell fixture relied on ambient execution policy. The daemon correctly logged the rejected script launches and reached its start limit. Fixtures now pass `-ExecutionPolicy Bypass` explicitly to their own PowerShell processes. Machine policy was not changed. The failed attempt's evidence was preserved before the clean rerun.

After evidence collection, guarded retirement verified removal of the disposable guest and its disks. The retained baselines remained stopped.

This qualification covers the CLI-installed service and runtime behavior. It does not qualify a production MSI, standard-user/session transitions, or all supported Windows baselines.

## Buffered write recovery

The writer now retains complete accepted records and the exact unwritten suffix after a partial disk write. Pending data is bounded per file to a 4 KiB batch or one oversized serialized record; the capture queue limits remain separate. New records for a failed file are rejected while a single scheduled flush retries with 1 to 30 second backoff. Recovery does not require new workload output or reopening the store. Failed close counts any pending records it abandons, including records partly written to disk.

Five repeated disk-full recovery/close tests and twenty short-write repetitions verify automatic recovery without new output, exact suffix continuation without duplicates, subsequent records, and pending-loss accounting. Full local race tests and vet validate integration. GitHub run `33976191703` passed for source `181472e01c9e9d2d4e5b2925bb401fabf0daf847`.

### Actual volume exhaustion

An offline disposable copy of the Server Core evaluation baseline, build `26100.33296`, passed `TestDisposableVolumeDiskFullRecovery` as SYSTEM. The fixture created a separate 64 MiB NTFS virtual disk, filled it with real writes until Windows returned a native disk-full error, and freed only its owned filler file. The journal recovered its pending record without a new write, query, wait, or store reopen; the rejected record was counted, subsequent output persisted, and no records were duplicated. The test passed in 2.16 seconds. Volume detachment and test-process exit were verified afterward.

The test binary used source `181472e01c9e9d2d4e5b2925bb401fabf0daf847` plus the new opt-in test, compiled locally without the race detector. Its SHA256 was `3ced8890bdd4fec7fce93c9d5e2f6398518f59c38267836f7aaad03d9d1ce139`; test-source SHA256 was `85a3f3467c10399d7c92e902da9e7ea7dbad29b626363a50fcf080f9ea340a38`. The executed `tools/lab/assets/journal-pressure.ps1` SHA256 was `360447dbf4a16d247f592f1acc2e1989f9eeba01e6f42a05de4d58471dcb7051`. Raw results and the initial fixture argument-parsing failure remain private. This qualifies the journal storage path on that baseline, not a complete service or installer scenario.

To reproduce, compile the journal package with `go test -c -trimpath`, put `journal.test.exe` on fixture CD media, and run `journal-pressure.ps1 -DisposableLab` as SYSTEM in an offline disposable Windows guest. Ordinary test runs skip the volume test unless `WINUNITD_TEST_JOURNAL_VOLUME` is set; the test also requires a separate 16-128 MiB volume labeled `winunitd-test`. A skipped run is not qualification.

## Query deadlines and admission

Journal queries now have a five-second default deadline and a context-aware entry point. Each store admits at most four query workers; cancellation returns the original cursor and leaves a blocked worker's slot occupied until the native call or lock wait finishes. Additional queries fail with an explicit capacity error. Scans check cancellation between records and files. Capture can continue during a stalled scan because scans hold no journal write lock.

Ten race-enabled repetitions cover four stalled scans outliving their callers, rejection of an additional request, continued append, eventual slot release, successful later queries, and cancellation while waiting for the flush lock. Full local race tests and vet pass. This is injected-stall evidence; it does not guarantee that Windows will interrupt a filesystem call or bound all manager memory/admission.

## Remaining qualification

Per-unit/per-SID operation gates now release their table entries after the last
holder or waiter finishes. The historical implementation failed the new
completed-name regression by retaining the first finished entry. Twenty
race-enabled repetitions cover 2,000 distinct completed names per run, canceled
waiters, independent units, and sixteen contenders repeatedly handing off one
gate without overlapping ownership. Entries remain pinned while any holder or
waiter references them. This removes historical-name growth; it does not cap
simultaneously accepted operations or replace R2 admission scheduling.

Twenty Windows race repetitions also cover both existence-watch rearm paths
returning a resource with an open error. Rearming stops, both old and partial
resources receive cleanup, failed cleanup remains retryable, and successful
closes are not repeated. The protected-native-handle partial-open regression
passes alongside these injected rearm tests.

Five aggregate-pressure repetitions filled the 16 MiB shared queue using five independent capture groups while storage was stalled. Each group stayed within 4 MiB, quiet-unit loss was counted, all queued bytes drained after recovery, and subsequent quiet output persisted. These historical checks exposed the former drop-new policy's limit: several noisy invocations can crowd out a quiet one at total saturation. The single-invocation cap is not a per-unit fairness guarantee.

- Windows identity/session scenarios and broader installer qualification remain open.
- Aggregate overload fairness is qualified below. Lifecycle admission and journal file/retention bounds remain open; actual volume exhaustion passed on Server Core and injected read stalls preserve bounded worker admission.
- Immutable configuration revisions and stale-event handling remain R2 work; service stop semantics remain R3 work.

## Delivered implementation detail

September 9, 2026: moved from the milestone checklist; these observations do not close R1.

live processes, in-flight operations, and active/unresolved SCM/task proxies retain their records and last accepted configuration. Accepted removals report `LoadState=unavailable`; status/log/stop remain accessible, restarts are suppressed, and new launches require valid configuration. R2 now rejects invalid/cyclic candidates as a whole, retaining the previous loaded definitions and start eligibility. Required Windows deletion/recreation regressions and portable proxy/trigger interleavings pass. Late watch installation is rejected and its handles closed; unavailable timers/hubs cannot activate companions. Accepted, service-invocation, and armed-trigger revision identities are recorded in R2; coordinator migration remains open.

process creation returns before oneshot completion; output capture attaches before waiting for exit. Required Windows tests verify all 5,000 stdout/stderr lines, no-newline fragments, startup timeout cleanup, and explicit stop while waiting. Existing completed-oneshot active state is preserved until R3. Post-creation unit launch failures preserve process/thread handles; if cleanup fails, the launcher returns the process with its error and the manager retains it for status/stop retry. Broader failure qualification remains coupled to R1.3.

retain process, job, and watcher ownership until cleanup succeeds; preserve status/stop retry and block replacement while cleanup is unresolved. Process/native/watch stop retries join pending adapter calls. Shutdown rejects new starts, accounts for accepted launches, and shares its deadline across user-host, unit, manager, and daemon-job cleanup. Partial launches/opens and failed native close calls remain owned. Implemented behavior and failure-injection evidence are recorded in [R1 evidence](R1-EVIDENCE.md). Accepted notification clients now remain owned across failed closes and late accepts, with manager stop retry coverage. Partial watcher/listener opens transfer unfinished cleanup to the manager, with portable and protected-handle regressions. Windows identity/session qualification remains open; stop remains forced Job Object termination until R3.

stdout/stderr capture emits UTF-8 fragments of at most 64 KiB before newline/EOF, with continuation/partial metadata in journal v3 and the logs API. Queued message data is capped at 4 MiB per invocation and 16 MiB per store, with 12,288 pending fragments per invocation and a 16,384-fragment shared queue cap. Capture keeps draining on overflow; unit status exposes drops and storage errors. Capture/sync waits and journal close have deadlines; at most four sync workers can remain blocked. Tests cover Unicode reconstruction, old journal readers, blocked writes/sync, overflow, failed writes, and recovery after a close timeout. A real child also drains large stdout/stderr and exits while storage is stalled, with observable drops within the invocation budget. Injected disk-full/short-write tests verify loss reporting and recovery after reopening; a missing final newline is restored without rewriting existing bytes or swallowing the next record. A noisy short-line invocation leaves capacity for another unit. Live write failures now retain the exact unwritten record suffix and retry with bounded backoff without reopening; failed close counts abandoned pending records. Actual NTFS volume exhaustion and automatic recovery passed as SYSTEM on a disposable Server Core baseline copy; exact identities are in [R1 evidence](R1-EVIDENCE.md). Queries now use a five-second default deadline and at most four workers; timed-out native operations retain their slots until completion. Injected scan/flush-lock stalls verify bounded admission and recovery. Remaining qualification includes fairness under aggregate overload and total lifecycle admission bounds.

## September 13 retained main-capture completion

A timed-out journal wait used to remove its capture record. A retry could then
succeed while the stream was still open, and replacement starts could accumulate
abandoned output readers. Main captures now have one owned completion, retained
through timeout. Replacement creation waits for prior output first. A failed
wait publishes independent journal cleanup, and reload retains an unavailable
record while its streams or failed cleanup remain owned. Close uses one aggregate
capture-wait budget and does not report success for unfinished accepted output.

The manager copies the exact capture identity for each wait. A late completion
cannot clear a replacement's capture. Completed store entries are retired, and
retries add no completion goroutines. This does not establish aggregate journal
file/retention fairness or qualify all storage and native handle failure cases.

Regressions cover repeated canceled waits, explicit and automatic replacement
attempts after self-exit, stop retry after EOF, removed-record retention, close
retry, stale completion and preservation of user journal metadata. Older hung
capture tests now require explicit cleanup instead of successful abandonment.

## September 13 aggregate capture fairness

The shared FIFO now uses bounded per-invocation queues with round-robin writing.
Under byte/record pressure, a less represented invocation can displace newest
pending records from a larger queue while preserving the victim's first pending
record and its remaining order. Every displaced byte/record is counted against
its producer. Queue bytes, records and invocation queues remain bounded; the
in-flight write participates in byte/record accounting.

Twenty focused race repetitions cover full aggregate pressure, exact displaced
loss accounting, quiet output before noisy queues drain, invocation ordering,
group admission and final accounting release. Three additional repetitions run
five actual child processes producing large stdout/stderr streams against a
stalled writer, then a quiet child. All children exit before storage recovery;
quiet output survives, queue limits hold, and owned captures complete afterward.
The full Windows race suite also passes, including previous storage-fault and
process-capture regressions. These checks qualify the queue policy; complete
journal file/retention and native failure matrices remain open.

Exact-source [Windows/Linux CI](https://github.com/PLN/winunitd/actions/runs/34723049847)
passed at `f5f4e37ccd40079d6ceb69c9d07d8e52bcbf433c`, merged in PR #154.
The real-child fixture produces 8.5 MiB across stdout/stderr per noisy child
(42.5 MiB total), with five children and a subsequent quiet child all exiting
before the stalled writer is released. Assertions enforce 4 MiB per invocation,
16 MiB aggregate queued message bytes and 16,384 aggregate records, including
the in-flight write. Separate deterministic tests cover 2,048 queued invocation
groups, exact victim loss accounting and quiet-writer progress before noisy
queues drain. This qualifies the queue policy, but does not complete R1.4's
combined memory/worker and status/stop responsiveness measurements under load.
It does not measure total process RSS or close R2.3/R5.3. The milestone remains
open until the broader acceptance criteria in issue #95 are demonstrated.

The same immutable Windows artifact also passed recovery and eight stop cases
as genuine SYSTEM and headless user on the disposable Enterprise LTSC baseline.
Cooperative stops took 1.176/1.202 seconds; forced helper/main/descendant cleanup
took 8.035/8.039 seconds within a 10-second budget. Repeatable/retained oneshots,
captured output/context, explicit retry ownership and no helper replay remained
covered by the automated suite; the native cases verified successful cleanup
and journal metadata. Pending timer intent survived broker crash with the same
activation ID in 2.359 seconds, and a second restart did not replay completion.
The activating user oneshot queried its own accepted PID/operation snapshot.
Final disable-linger released the user manager/helper/profile with no
resurrection. Raw scripts, results, CI identity, manifest and hashes are retained
privately. This selected native matrix does not qualify MSI servicing or R7.
