# R2 initial evidence

## Service definition ownership

A valid reload previously replaced the definition used to address an existing
native SCM service or scheduled task. The retargeting regression fails against
the preceding implementation: status queries the new, inactive target even
though the old target remains running. Stop could then leave that old target
running. Changing a process service into a native proxy had the same routing
problem.

Service launch now captures its definition before adapter side effects. Reload
continues to load the latest definition separately. Status overlays, resource
limits in status, stop/shutdown routing, exit/watchdog cleanup, and automatic
recovery consult the captured definition. Successful explicit cleanup releases
it. Failed native start or stop does not authorize abandoning the old target;
an explicit start targeting a different native identity requires stop first.
A fresh explicit process start adopts the latest definition after prior process
cleanup succeeds. Automatic recovery retains the original service configuration,
including its restart policy and start-limit settings.

The Windows manager tests use isolated fake SCM/task adapters and process
fixtures. They cover:

- Valid target replacement for both native proxy types, followed by stop and a
  fresh start using the new target.
- Every directed change between process, SCM, and scheduled-task service types,
  with list status, explicit stop/start, and shutdown.
- A native start that fails after starting its resource, a failed first stop,
  rejection of replacement starts, and successful cleanup retry.
- Reload while a native start is paused inside its adapter, followed by cleanup
  of the original resource after the adapter completes.
- Process exit after reload: automatic recovery launches the old command despite
  the new definition disabling restart; explicit stop/start launches the new one.

## Validation

Twenty race-enabled repetitions of the five reload ownership tests pass on the
local Windows development environment. The full repository race suite
(`go test -race -parallel 1 ./... -timeout 180s`) and `go vet ./...` pass. The
paused-launch regression was added afterward and passed in the focused twenty-run
set. No live pilot deployment was changed for these checks.

## Start plan definitions and stop precedence

Start plans now capture the member definitions alongside the dependency graph,
under the same manager lock. Pending members retain their runtime records until
the transaction finishes. A later valid reload cannot substitute a new command
into an already accepted graph; a fresh start plan uses the new definition.
Current alpha removal/invalid-file admission still rejects a missing or
unavailable member before launching it.

Each planned member captures a stop epoch. Stop admission invalidates older
pending starts, and transaction result application excludes those invalidated
members. A superseded transaction returns an error without overwriting the
completed stop's state or error. A start requested after that stop remains
permitted. This changes the older mid-transaction stop regression's result:
the canceled pending target is now reported as canceled instead of successful.

Twenty race repetitions cover a blocked dependency spanning valid reload, stop
of a member waiting for its dependency, and stop of a member that has already
started while another branch is pending. Definitions and stop epochs are internal;
this change does not introduce revision IDs or an operation-query API.

## Per-unit completion publication

A blocked multi-unit start reproduced a stale result overwrite: one process
exited and reached failed state, then completion of the remaining dependency
made the transaction summary mark the exited unit active. Start adapter outcomes
are now published per member while its operation gate is still held. Final
transaction application only handles members never launched and still matching
their original generation and stop epoch. It cannot replay completed members or
clear errors produced by a later exit/recovery operation.

A corresponding stop regression stopped one member, paused another member's
cleanup, and failed a new start of the already-stopped member. The old stop
summary incorrectly replaced the new failed state with inactive. Stop and
shutdown now leave runtime publication to their existing generation-checked
per-unit stop operations; transaction summaries are returned as outcomes only.
Both deterministic regressions pass twenty race repetitions. Graph planning and
dependency failure reporting remain separate from observed runtime state.

## Watch definition and callback ownership

Armed registry, event-log, and path watches retain the definition used to open
them. A PathExists regression reproduced evaluating new conditions against old
watch handles after reload. The captured conditions remain in effect until a
fresh activation opens replacement watches.

Callbacks carry the exact watch instance and generation. A stale existence
probe or failure cannot update or close replacement watches. Watch-originated
start plans validate that source at admission and again after waiting for the
destination operation gate; stopping the source cancels a queued companion
launch. Initial already-satisfied PathExists activation uses the same origin
checks. Four focused regressions pass twenty race repetitions using isolated
fake watches, delayed probes, and a blocked destination gate/launcher. A source
stop preserves an already-admitted companion launch and its result.

Native path testing also exposed an invalid test assumption: several filesystem
notifications from one write can admit another start after a companion stop.
Diagnostics confirmed a newer generation with stopping cleared. The native test
now stops the watch before asserting a stable stopped companion, rearms it, and
requires an additional counted execution after the next write. Twenty Windows
race repetitions pass.

## Dependency qualification finding

The full manager run uncovered a go-winio listener-close hang during watchdog
recovery. The accepted repository-relative patched copy passes 100 watchdog/restart
repetitions, the manager race suite, vet, build, and vulnerability scanning.
Its deterministic cancellation regression passes 100 repetitions and fails when
the original cancellation branch is restored. See the accepted
[pipe-listener decision](PIPE-LISTENER-DECISION.md) and upstream tracking.
The full-suite rerun also exposed a journal test deadline expiring before scan
admission. That bounded-worker test now cancels after confirmed admission; the
separate deadline/flush-lock regression remains. The corrected full repository
race suite passes with the checked-in replacement. Lab artifact admission also
accepts the new manifest and requires its dependency hash and license artifact.

## Timer deadline ownership

A dequeued timer deadline now carries its armed-instance identity and schedule
generation through consumption. Schedule generations are unique across rearming
the same name, so an old heap entry cannot match a replacement timer either.
Regressions reproduced both stale-dequeue consumption and an old calendar entry
firing a replacement schedule early. The replacement's real deadline still fires.
Twenty race repetitions of the complete timer package pass.

Timer callbacks now carry a unique arm token and the captured companion name.
Stop/rearm or refresh invalidates an old callback and its eventual success result;
an old result cannot write the replacement's persistent last-success timestamp.
Watch and timer starts share origin checks at plan and adapter admission. Stopping
a timer cancels a queued companion but preserves one already admitted to launch.
Valid reload retains the armed timer's schedule and target; a fresh activation
adopts the latest definition. Three manager regressions and the timer package
pass twenty race repetitions, including a controlled callback completion after
replacement and reload across two different relative timer schedules. The full
repository race suite and vet also pass after this callback-ownership slice.
An additional timer-started oneshot shutdown check passes twenty race repetitions:
shutdown cancels the long startup wait, drains the timer callback, and releases
process ownership without advancing the fake clock to the startup deadline.
This confirms the existing cancellation path; no shutdown change was needed.

## Trigger admission and publication

A valid reload could dispose a watch after the adapter installed it but before
its start transaction published Active. A controlled adapter/publication gap
reproduces the failure. Reload now retains the matching watch generation while
its lifecycle operation is in flight; explicit stop still closes it. The
regression and unavailable-trigger checks pass twenty race repetitions.

Alternating timer and path activations previously reset a service's start budget,
allowing a burst to bypass StartLimitBurst. Trigger-originated plans now retain
and enforce the shared service budget before launch; ordinary operator starts
keep the alpha reset behavior. A mixed-source regression exercises both orders,
window expiry, and operator reset, alongside the existing automatic-recovery
limit tests. The focused set passes twenty race repetitions. This bounds launch
attempts, not callback goroutines or overall operation admission; those remain
R2.3 work. The full repository race suite and vet pass. The journal cancellation
regression now allows ten seconds for scan admission under host contention and
separately bounds cancellation response while that scan remains blocked.

## Transaction worker bound

Start and stop transactions now default to at most sixteen concurrent adapter
calls. The completion channel is bounded to the worker count, and accepted calls
are drained before returning. Internal callers can select a positive limit with
ExecuteWithLimit/ExecuteStopWithLimit; invalid limits fail before side effects.

A 48-member plan cancels after its third admitted start: exactly three successful
completions are retained and the other 45 members report cancellation. A serial
48-member stop plan retains every failure and attempts every member. Existing
parallel-ordering and dependency tests remain enabled. Twenty race repetitions
of the core package, the full repository race suite, and vet pass. This does
not by itself cap simultaneously accepted plans, RPC connections, or retained
graph/result memory. The subsequent admission slice below adds a plan bound.

## Manager start admission and overload recovery

Each manager now admits at most 32 simultaneous start transactions by default,
checked before graph-plan allocation. Config.MaxStartTransactions permits a
positive override for embedded callers; zero selects the default and negative
values are rejected. The daemon currently uses the default. Rejected operator
requests receive the existing failed RPC error with a capacity-exhausted message.
Stop and shutdown do not need a start slot. Accepted plans retain their slot
until every admitted adapter completion is drained, including failure paths.

Native watch loops retain an activation rejected for capacity, wait for a capacity
or ownership change, and revalidate the exact source before retrying. Stop,
shutdown, close, and reload wake these waits. No separate retry worker is spawned.
Timer dispatch is bounded to 32 callbacks per engine and one per arm. Undispatched
deadlines remain queued. A timer rejected by manager admission retains its exact
activation and retries after 250 ms of monotonic time; replacement/disarm invalidates
that retry. A capacity race after dequeue puts the original calendar occurrence
back, rather than calculating a later occurrence and losing the pending one.

Focused race tests run twenty times cover repeated operator overload, an independent
stop while full, eventual one-shot timer activation, cancellation of a waiting watch,
slot release after invalid plans, callback capacity, monotonic retries, stale rearm
identities, and capacity filling after calendar dequeue. Full repository race tests
and vet pass. Pending timer retries remain in memory; crash recovery and durable
intent are R5 work. RPC connection counts, stop request admission, automatic recovery
worker totals, configured watch counts, and graph/result memory remain separate
bounds. This is not closure of R2.3 or the coordinator milestone.

## Offline LTSC daemon qualification

The clean `984fbc95e2d2b2f6ea6aceed572947d0071136eb` Windows artifacts from
[CI run 34039331915](https://github.com/PLN/winunitd/actions/runs/34039331915)
passed SYSTEM qualification on a disposable Windows 11 Enterprise LTSC Evaluation
baseline copy, build 26100.9168, with four vCPUs and 8 GiB RAM. Native Windows
and Linux cross-build manifests match exactly; all four artifact hashes were
verified before transfer and by the guest. Maintenance routing was disconnected.

The controller-driven service install, enabled workload reboot recovery, new
invocation identity, single session-0 child, and SCM stop checks passed. The
additional [admission fixture](../tools/lab/assets/admission-checks.ps1) held 32
real oneshot starts pending through separate named-pipe clients. A 33rd start
was explicitly rejected before child creation. Stopping one pending unit worked
while admission was full; a fresh start reused that slot while the other 31
remained pending. Releasing the fixture completed all remaining accepted starts,
and SCM stop left no fixture children.

The admission fixture SHA256 is
`72371ba1dc9949549a6f2cbb73a37e0612746305b34c520bbf86abe5bd1f6ee7`.
The existing runtime fixture also passed all 5,000 stdout and 5,000 stderr records,
131,073 bytes of unterminated output, live reload deletion/status/log/stop/recreation,
and ten notify-ready/stop/reopen cycles. Its SHA256 is
`7faf7efe78ee0b73e954492492e8a4f2cd5cef9461dbb607c486594983d6f153`.
Raw logs and ownership mappings remain in the private operator workspace.

These are real daemon checks of the default admission budget and retained runtime
behavior. They do not qualify persistent timer recovery, total memory bounds,
interactive users, MSI servicing, or the full supported-platform matrix. Baseline
cloning and fixture preparation were supervised; this is not generalized-image
provisioning evidence.

## Atomic reload acceptance

Invalid unit files now reject the entire candidate instead of accepting its valid
subset. Ordering cycles also reject the candidate graph. Neither rejection
changes loaded definitions, enablement, runtime records, or the accepted graph.
A rejected cold load leaves control available with no accepted candidate units.
The reload response contains the parser/scope diagnostics or cycle; `Loaded=0`
means this attempt accepted no units, not that a previous revision was discarded.

Reload, enable, and disable serialize their configuration I/O and acceptance
separately from the lifecycle mutex. Unreadable unit or enablement directories
fail without replacing the graph; only a genuinely absent directory is empty.
Windows can return a not-found error for reading a regular file as a directory,
so the loader checks absence before accepting that interpretation.

Regressions reproduce mixed valid/invalid replacement, a rejected removal,
partial cold startup, a cyclic replacement, and unreadable configuration roots.
The corrected behavior and existing enable/disable, real-process reload, and
native-proxy ownership cases pass twenty race repetitions. A valid removal
still marks a retained live runtime unavailable and prevents new activation.
An invalid replacement retains its last accepted loaded definition and start
eligibility, superseding the earlier alpha behavior that marked it unavailable.
The full repository race suite and vet pass after this acceptance change.

This does not provide a filesystem snapshot across external editors,
transactional rollback of enable-link writes, or full graph
construction outside the lifecycle mutex. Missing required dependencies remain
plan-admission errors. Durable configuration diagnostics and cold-start handling
of filesystem I/O errors remain separate work.

## Configuration revision identity

Accepted reloads and enablement graph swaps now receive an opaque revision ID
with a random daemon-instance namespace and a monotonic acceptance sequence.
Machine status and loaded unit status expose `ConfigRevision`; the reload
response identifies the accepted revision even when a candidate is rejected.
An unavailable retained unit has no loaded revision. Rejected candidates leave
the identity unchanged, while an accepted unchanged-content reload advances it.
Revision IDs identify acceptance events, not content hashes or archival lookup
keys; different IDs do not prove a particular unit's content changed.

Service status exposes `InvocationConfigRevision` for its last captured service
invocation, including native proxy start attempts. Accepted plans retain their
captured revision through delayed launch and reload. Automatic service recovery
keeps the captured revision; an explicit new start adopts the accepted revision.
Confirmed stop retains the last invocation ID/revision for diagnostics. Failed
process-start preparation cannot relabel the previous invocation with a newer
revision. The CLI displays both fields; the RPC fields are optional additions.

Twenty race repetitions cover namespace separation, rejected and accepted
reloads, enable/disable, live removal and stop, queued-plan capture, automatic
recovery, and failed preparation followed by cleanup and a fresh start. This
does not implement operation-history queries, persisted revision storage,
or the lifecycle coordinator.
The full repository race suite and vet pass with these optional status fields.

## Armed trigger revision reporting

Timer specs and installed registry/event-log/path watches capture the accepted
plan's revision at arm/open. Unit status exposes it as `ArmedConfigRevision`,
separate from the latest loaded revision and service invocation revision. A valid
reload leaves an existing arm's identity unchanged; successful disarm/cleanup
clears it, and a fresh activation captures the accepted revision. A delayed
native open retains its original plan revision even when reload finishes before
the handle is installed. Failed native close retains the installed watch's
identity along with its ownership.

The timer listing now reports the captured armed companion and both loaded/armed
revision IDs. Previously, reload could make the listing name the new companion
while the existing arm still activated the old one. A regression reproduces
that discrepancy and verifies the actual firing behavior through stop/rearm.
Timer metadata comes from the same engine snapshot as its next/last timestamps.
Aggregate status still combines manager and adapter snapshots; it is not yet
the immutable coordinator snapshot required by R2.

The trigger capture/reload/rearm and delayed-open regressions pass twenty race
repetitions, as does the full timer package. Repetition also reproduced a cache
test race on the previous engine: the assertion counted asynchronous callback
rescheduling as status work. It now measures only the existing status-specific
deadline hook and retains its next-deadline/cache/heap checks.
The full repository race suite and vet pass after the trigger reporting change.

## Lifecycle completion and recovery identity

Recreating a removed unit can reuse its numeric generation. Regressions
reproduced a late watchdog timeout terminating the replacement and a late
restart request changing its state when callbacks carried only name/generation.
Watchdog loops and recovery requests now carry the exact runtime-record identity
as well as generation. Process-exit completion is a typed payload containing
that identity and the captured service policy; its serialized decision rejects
stale records before changing lifecycle state or requesting recovery.

Recovery validates the identity at admission, after its delay, and inside launch
admission after acquiring the unit gate. It also retains cancellation across
that gate wait. A controlled interleaving queues recovery behind a newer explicit
attempt that fails: the stale worker must not launch again. Removing the final
ownership/cancellation check reproduces that extra launch. Watchdog installation
checks the current generation before replacing its cancellation handle.

Twenty race repetitions cover recreated-record watchdog/restart/exit delivery,
recovery delayed by the unit gate, captured recovery configuration, explicit-stop
cancellation, and failed notification/watchdog cleanup. The full repository race
suite and vet pass. Blocking cleanup still runs outside the lifecycle decision;
this is an initial typed completion path, not migration of every state writer
or a completed coordinator/event-admission design.

## Reload and recovery build qualification

Source `a6d7253289efb22f1ba56bd525e4eb6db2336a52`, GitHub CI run
`34042191013`, passed on a disconnected disposable Windows 11 Enterprise LTSC
Evaluation guest, build `26100.9168`, running as SYSTEM. Native Windows and Linux
cross-build manifests matched, and all four artifact hashes were verified.
The controller service/reboot smoke, runtime output/reload/notify checks, and
32-start admission scenario passed again against this exact build.

The new configuration scenario also passed through the real control pipe:
an invalid candidate accepted neither its changed definition nor its new unit;
a subsequent valid reload retained the live invocation's original revision;
an explicit fresh start adopted and executed the new revision; removing the
live definition preserved status and stop access while rejecting new starts.
SCM stop left no fixture children. Configuration fixture SHA-256:
`01e1109df77bcc793f987b76f3ff9c99d4051cae7391547fb7983d2c7d0016d2`.

All three post-smoke scenarios ran through the controller's `checks` command,
which admitted exact source/fixture identities and complete assertion sets and
collected private evidence. Guarded retirement verified guest and disk removal.
Baseline cloning and inherited-fixture preparation were supervised. These checks
do not qualify later commits, MSI servicing, or the broader identity/session
matrix; deterministic recovery interleavings remain covered by package tests.

## Control-server lifetime

A listener-failure regression reproduced an accepted handler remaining active
after `Serve` returned while the caller's context was still live. The server now
derives a serving context and cancels it on every return after admission begins.
This releases its cancellation waiter, closes accepted control connections, and
signals cooperative handlers without cancelling the caller's context. Fifty
race repetitions verify handler cancellation and connection release; the full
repository race suite and vet pass. This does not add bounded connection
admission, wait for uncooperative handlers, or implement durable operation IDs.

## Explicit restart stop precedence

A delayed native-stop regression reproduced an explicit restart launching again
after a later stop had already been accepted. Restart now retains its runtime
record and expected stop epoch across cleanup and start-plan admission. Any
additional root stop invalidates the pending start phase and queued dependency
launches. Already admitted adapter work retains the existing cleanup/completion
rules. An explicit restart still follows explicit-start budget policy, rather
than charging the automatic trigger retry budget.

Twenty race repetitions cover stop during native cleanup, stop while a restart
waits for a dependency gate, and repeated explicit restart with a one-start
automatic budget. Overlapping restart/stop requests can supersede each other;
the captured multi-phase plan below extends this initial stop-precedence change.
The full repository race suite and vet pass after this change.

## Captured restart plans

Restart now captures its stop graph, the active/activating reverse members to
restore, and a validated multi-root start plan with definitions/revisions before
teardown. Active `PartOf` members return even without a root `Wants` edge;
inactive reverse members stay inactive unless the start graph selects them.
Both phases use the same admission slot, and stop-only records remain retained
through delayed cleanup. A later stop of any captured stop member invalidates
the pending start phase and queued launches.

Accepted removal also invalidates the captured restart origin. A follow-up
regression reproduced a queued dependency launching after the root definition
was removed; the unavailable-record check now rejects that launch. Removal/stop
dependency-gate cases and restart regressions pass twenty race repetitions.

Regressions reproduced active `PartOf` services remaining stopped, an invalid
start plan or full admission pool interrupting the running service before
reporting rejection, and reload retargeting a restart during native cleanup.
The first case failed before the change; the other cases also failed against a
private overlay of the preceding implementation. These now pass twenty race
repetitions, together with stop precedence and explicit-start budget policy.
This establishes captured restart phases and admission, not queryable operation
history, whole-operation deadlines, or the completed lifecycle coordinator.
The full repository race suite and vet pass with captured restart phases.

## Stop cleanup and completion events

Stop workers now deliver typed cleanup and final-completion payloads carrying
the exact runtime record, generation, process, and operation error. Cleanup
ownership is published before releasing the unit gate; later journal draining
cannot update a replacement operation's state.

A controlled failed stop followed by a successful retry reproduced the first
request incorrectly reporting success when its journal wait finished late.
Completion now returns that request's original failure while leaving the newer
successful stop's inactive state intact. Twenty race repetitions cover this
interleaving, failed-stop ownership/retry, later failed-start state preservation,
and releasing the unit gate during journal draining. This advances typed event
delivery; it does not introduce retained operation-history queries.

Start-plan workers likewise deliver a typed member-completion payload through
the lifecycle decision path while retaining the unit gate. Accepted definition,
record, stop epoch, and origin checks precede publication; transaction summaries
still cannot replay completed members. This centralizes those completion writes
without changing the existing start-result semantics. Other adapter/admission
state writers and immutable aggregate status publication remain to be migrated.
The full repository race suite and vet pass after these completion changes.

## Queryable transaction history

Validated/admitted start, explicit stop, and explicit restart transactions now
receive operation IDs, with running and completed snapshots available through
`winctl operation`. Unit status reports the last participating operation;
admitted RPC failures carry their ID without changing the error code. History
retains pending records and the latest 256 completed records, with errors capped
at 4,096 UTF-8 bytes. Explicit stop has separate default admission of 32
transactions; shutdown bypasses start and stop admission. Stop plans retain
participating records across delayed cleanup and reload.

Twenty race repetitions cover disconnect during an admitted native start,
reconnection after completion, pending-record survival while completed history
is evicted, immutable query copies, bounded Unicode errors, rejected requests
without history entries, stop overload and independent start capacity, shutdown
bypassing that admission, CLI exit codes, and authorization of the new method.
A failed stop and successful retry retain separate queryable outcomes. Existing
protocol round trips and the full repository race suite and vet pass.

[Operation history](OPERATIONS.md) documents optional protocol fields, older
server behavior, retention, and limits. Automatic process recovery and daemon
shutdown are not transaction-history entries. History does not survive manager
restart; operation contexts, aggregate deadlines, and the remaining lifecycle
coordinator are still pending.

## Limits

These are incremental R2 ownership and admission slices, not the completed v2
coordinator. They do not add immutable status snapshots,
admission bounds for every operation class, or a fully migrated typed-event
coordinator.
Existing generation
checks and lifecycle writers remain in place; the full interleaving matrix and
source audit are still required. The isolated adapter tests and LTSC daemon checks
do not complete SCM/Task Scheduler proxy qualification, the supported Windows
matrix, or MSI maintenance.
