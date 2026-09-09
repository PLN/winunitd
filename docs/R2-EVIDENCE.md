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
restart. Operation contexts and aggregate deadlines were pending at this stage;
the later operation-lifetime slice below supersedes that limitation. The lifecycle
coordinator remains pending.

### Windows operation qualification

On September 6, 2026, clean commit
`87e46660cdce122ba217d04b0d70026e0cc13407` from successful CI run
`34045155713` passed isolated Windows 11 Enterprise LTSC Evaluation
`26100.9168` qualification as SYSTEM with maintenance routing disconnected.
Native and cross-built artifact manifests, hashes, and sizes matched before
installation. SCM installation, reboot recovery, runtime, admission,
configuration, and operation scenarios passed.

The operation fixture SHA-256 was
`a1fa090a07d0d4b4cb4f2d26dbb1b4842733bc8c5025a9a57641d667fd860641`.
It verified active `PartOf` restoration while an idle member stayed inactive,
successful restart queries, failed outcomes surviving definition removal,
independent later stop outcomes, history expiry after manager restart, and no
fixture process surviving SCM stop. Raw evidence remains outside the repository.
This run predates compatible start coalescing and does not qualify that change.

## Compatible start coalescing

Concurrent explicit starts now join an admitted start when their unit record,
accepted revision, and stop epoch match. They share the operation ID and outcome
without consuming a second transaction slot. Joined callers receive separate
reply copies; cancelling a joined wait leaves the original operation running and
returns its ID for query. New revisions, intervening stops, trigger activations,
and explicit restarts do not join incompatible work.

A blocked native start with a one-transaction limit previously rejected a second
compatible request for capacity exhaustion. It now joins. Twenty race repetitions
cover cancellation of that wait, shared successful outcomes without mutable reply
aliasing, and rejecting a new revision or intervening stop from joining an old
start. The full repository race suite and vet pass. The original
caller's context is addressed by the next slice; control-connection bounds remain
separate work.

## Accepted operation lifetimes

September 7, 2026; [R2.4 issue #98](https://github.com/PLN/winunitd/issues/98).
Accepted start/stop/restart transactions now own their contexts and aggregate
cancellation budgets. Caller or serving-context cancellation ends the response
wait without canceling accepted work. Shutdown/close cancels launches and restarts;
accepted stops retain their own cleanup lifetime. DeadlineAt and CancellationReason
are optional protocol fields with CLI output; the protocol version and existing
success/error shapes are preserved.

Deadline cancellation and member completion publish under the same manager lock.
Completed members are preserved; incomplete launches disarm recovery and retain
ownership. Late process/proxy/watch completions are cleaned up under the unit gate.
The operation captures the actual launch generation, which may differ from plan
acceptance after a queued stop. Cleanup failures retain truthful status and stop
retry. A response timeout never implies that native handles have been released.

Deterministic regressions cover first-caller and serving-context cancellation,
pre-admission rejection, expired unit-gate waits, late process creation with retained
admission, preserved completed dependencies, stop/restart caller-versus-deadline
behavior, and one budget across ordered stop members. A queued-stop/new-launch
case proves that cancellation follows the actual invocation generation. Delayed fake
SCM/task starts and watch opens verify cleanup after late native completion. Existing
shutdown and cleanup-failure regressions now query final operation completion
before inspecting late resources, rather than treating a canceled response wait
as a cleanup barrier. Existing joined-caller and protocol compatibility tests remain.

The focused operation/CLI regressions passed ten local Windows race repetitions;
queued-generation, partial dependency, stop/restart and late native/watch cases passed twenty. The
full local Windows race suite and vet passed with Go 1.27.1. Final cross-platform
CI and exact revision identity are recorded in the linked issue/PR. No new
SYSTEM/session, VM, MSI or pilot qualification is claimed for this change.

[Operation behavior](OPERATIONS.md#accepted-work-and-cancellation) specifies budget
calculation, cancellation ownership, pending-cleanup diagnostics and remaining
resource-bound limitations. Native calls can outlive cancellation while retaining
their existing cleanup owner; this is not a hard real-time termination guarantee.

## Transaction outcomes without lifecycle replay

September 7, 2026; [issue #96](https://github.com/PLN/winunitd/issues/96).
The graph executor reports never-launched failures synchronously through an
optional rejection observer. The manager checks the accepted record, generation,
stop epoch and origin, then publishes one member outcome. Adapter completions
still publish under the unit gate; canceled gate waits publish rejection too.
The final transaction Run is no longer applied to unit lifecycle state.

A deterministic regression leaves an independent worker blocked, observes the
root's dependency failure immediately, stops that root, and then releases the
worker. The old transaction retains its failed outcome without rewriting the
new stop. Further regressions preserve replacement invocation diagnostics and
failure visibility for expired gate waits. These and the existing dependency and
late transaction tests passed twenty local Windows race repetitions. The full
local Windows race suite and vet passed. Exact reviewed revision and integration
checks are recorded on the linked issue/PR; no new VM/MSI qualification is claimed.

The [lifecycle writer inventory](LIFECYCLE-WRITERS.md) maps remaining mutation paths
to Design v2 invariants and tests. It explicitly retains coordinator admission,
worker-state migration, UserHost coordination and immutable aggregate snapshots
as open work. This slice does not close #96 or R2.

## Automatic replacement generations

September 7, 2026; follow-up to the writer audit in #96.
Automatic launches previously reused the prior invocation's generation. A delayed
watchdog, restart request or process-exit event could therefore match a replacement
on the same runtime record. A fake-clock regression demonstrated all three failures.
Every real launch now advances the generation after recovery admission checks;
redundant starts still preserve the live invocation and watchdog identity.

The new regression starts a service, observes an automatic replacement, then
delivers each old callback. The replacement remains live and active without new
failure diagnostics or recovery. These cases and existing recovery, restart and
watchdog regressions passed twenty local Windows race repetitions. Full-suite and
integration evidence is recorded in the associated PR. This fixes an identity
boundary; it does not claim the remaining coordinator migration is complete.

## Watchdog and process-exit cleanup events

September 7, 2026; next migration slice for #96.
Watchdog timeout and process-exit workers now request a captured cleanup effect,
perform native cleanup outside the manager lock, and deliver a typed result.
Lifecycle handlers own failure state, uncertainty, process removal and recovery
permission. Results validate runtime record, generation and process ownership.
Reload does not change the captured cleanup or recovery configuration.

Stale successful and failed cleanup results are tested after stop and replacement;
neither can release the new process, change its diagnostics or authorize recovery.
These regressions passed twenty local Windows race repetitions, alongside repeated
watchdog/exit/recovery coverage. Full-suite and integration results are recorded
in the associated PR. Notification/watch handle helpers, launch/readiness, reaping
and other rows in the writer inventory remain to migrate; this is not R2 closure.

## Stop-scope precedence at admission

September 7, 2026; follow-up from #96's stop-writer audit.
Accepted stop/restart operations now invalidate pending activation and suppress
recovery for every member of the captured stop plan before dispatching workers.
Previously, an ordered member could still enter automatic recovery while an earlier
member's stop blocked. The new regression reproduced that behavior for both stop
and restart before the fix, and passes afterward.

Plan validation and capacity checks still happen before suppression. Ordered
workers use the accepted scope without incrementing its stop epochs again, keeping
the restart's own captured start plan valid. Standalone internal stops retain their
pre-gate suppression helper. Rejection, restart and queued-generation tests passed
twenty local Windows race repetitions; full-suite/integration evidence is recorded
in the associated PR. This advances stop precedence without closing #96 or R2.

## Stop, failed-process and handle cleanup boundaries

September 7, 2026; continuation of #96.
Per-member stop admission now captures a retained runtime record, exact generation,
process, invocation definition and watchdog cancellation in one cleanup effect.
Workers perform native teardown and journal waits from that capture; lifecycle
handlers retain/release records and publish the existing stop results.

Failed-process reaping now captures the runtime record as well as generation and
process, revalidates after its gate wait, and reports cleanup results to a handler.
Notification and watch-close results also use typed handlers. Those match the
exact retained handle rather than a generation, since retries may advance the
operation generation while joining the same pending native close.

Stale successful/failed reaping results are covered after stop and replacement,
with twenty local Windows race repetitions. Existing stop, close, cleanup-failure
and restart regressions passed three repetitions. Full-suite and integration
results are recorded in the associated PR. These boundaries preserve the current
cleanup contract; coordinator admission and remaining launch/trigger/configuration
writers remain open.

## Recovery admission and native completion decisions

September 7, 2026; continuation of #96.
Recovery acceptance and watchdog registration/detach now use lifecycle handlers;
workers retain delay, probe and launch I/O. Native SCM/task completion carries the
captured runtime identity and checks cancellation, shutdown and stopping before
publishing lifecycle success or an activation observation to the timer engine.
A concurrent explicit stop still preserves the native adapter outcome; operation
deadline/cancellation remains owned by the operation record.

The extended late-native regression demonstrated that both adapters previously
scheduled OnUnitActiveSec after their operation deadline. Both now clean up late
work without scheduling that activation. Accepted observations retain their
acceptance timestamp while scheduler/persistence work stays outside the decision.

Native/deadline/restart regressions passed ten local Windows race repetitions;
recovery/watchdog/scope coverage passed five. Full-suite/integration results are
recorded in the associated PR. Remaining launch, readiness, trigger and configuration
writers and aggregate snapshots still prevent closure of #96.

## Captured launch admission

September 7, 2026; continuation of #96.
Launch admission now produces a retained effect with the exact runtime/generation,
accepted configuration and previous-invocation cleanup policy. Dedicated handlers
publish previous cleanup, service attempt accounting, invocation metadata,
notification adoption and partial-launch failures. Late partial creations remain
owned even when activation has been superseded.

Liveness is observed by the worker outside the manager mutex, with the unit gate
retained and the record/process revalidated at admission. A delayed-liveness
regression reproduced a manager-wide admission stall before the change. Afterward,
an independent target starts while that observation remains blocked, and the
redundant start does not replace its live process. The regression and late cleanup
coverage passed twenty local Windows race repetitions; launch/stop/restart/operation
coverage passed three. Full-suite/integration evidence is recorded in the PR.

Successful process adoption and readiness/oneshot completion remain direct writers
for the next slice. This is not closure of #96 or the aggregate snapshot work.

## Process adoption and readiness completion

September 7, 2026; continuation of #96.
Successful process adoption, late-launch cleanup, oneshot wait registration/results,
readiness failure/cleanup and final activation now use lifecycle handlers.
Supervision workers no longer assign runtime lifecycle fields directly. Their
blocking process, readiness, job and journal work remains outside the decisions.
Late creations are adopted before cleanup under the retained record and unit gate.

Final activation validates cancellation, stopping, exact runtime/generation and
process identity before producing timer/watchdog effects. Stale readiness, oneshot,
late-launch cleanup and activation results are tested after stop and replacement;
the replacement remains live with unchanged diagnostics. These cases passed twenty
local Windows race repetitions; notify/oneshot/late/stop/restart/operation coverage
passed three. Full-suite/integration evidence is recorded in the associated PR.

Trigger/configuration/user-host decisions, coordinator admission and aggregate
snapshot publication remain open in #96. This does not change beta oneshot syntax
or claim new native qualification.

## Lifecycle migration SYSTEM qualification

September 7, 2026. Reviewed source
`1293a006c433ec9bd685615912132f8c45cdd76c`, from successful GitHub CI run
`34155003017`, passed offline SYSTEM qualification on Windows 11 Enterprise LTSC
Evaluation build **26100.9168**. Merge commit
`2ea79ad44751a9a14f6caf7bdcb15b0d8dfd704a` has the same source tree.
The clean Windows/amd64 artifact manifest, all file sizes and SHA256 hashes,
module hashes and dependency source tree hash were verified before admission.

| Binary | SHA256 |
| --- | --- |
| winunitd.exe | `cdddcb1142598816780e9578c4e8d55646c31c800489cdd2b087120c99486322` |
| winctl.exe | `6f3a7815851d2a4a7cf26df516d33037c6c64e5456e8fe088d773ddc34d5bfff` |
| winunit-notify.exe | `8db74ea0085cbb735c889671002b6a79fafe5f4591c764b388219bcf08b4fe3c` |

The disposable guest was a supervised full copy of the stopped maintained baseline.
Preflight confirmed SYSTEM identity, active evaluation with more than one day
remaining, Secure Boot, synchronized clock and no default route. The inherited
stopped, disabled lab fixture was archived before testing.

Service installation/start/stop, reboot recovery with a new invocation and exactly
one session-0 fixture process, and SCM-stop cleanup passed. Controller-driven
runtime, admission, configuration and operations checks passed all **21** expected
assertions, with exact source and fixture identities verified. These cover output
handling, reload/removal/recreation, notify cycles, saturation rejection and stop
access, slot reuse, configuration revision retention and adoption, PartOf restart
participation, retained operation outcomes and history expiry. Raw results and
transcripts remain private. Guarded retirement verified guest and disk removal.

The accepted baseline Windows Update anomaly remains as documented in
[TEST-LAB.md](TEST-LAB.md#maintained-baseline-observations--september-5-2026).
This adds real-daemon regression evidence for the migration slices through process
adoption/readiness; it does not close #96 or establish fresh installation,
generalized provisioning, MSI servicing, interactive-session or release acceptance.

## Watch lifecycle decisions

September 7, 2026; continuation of #96. Watch admission, failure, disarm,
partial-open ownership, cleanup retention and configuration reconciliation now
use lifecycle handlers. Path-existence observations update their latch only
through the current armed-generation handler. Blocking open/close and predicate
I/O remain outside lifecycle decisions.

A deterministic regression reproduced a late watch failure replacing an accepted
stop decision with failed state and a new diagnostic. Failure admission now
rejects observations after stop, removal, shutdown or generation replacement.
Twenty race-enabled repetitions passed for stop precedence, stale predicates and
failures, and independent start while watch cleanup is blocked. Existing watch
and path tests, the full local Windows race suite
(`go test -race -parallel 1 ./... -timeout 180s`) and `go vet ./...` passed.
This slice is not included in the preceding SYSTEM
artifact qualification; coordinator routing and the remaining writer inventory
are still open.

## September 9 review follow-up

The watch slice passed hosted Windows/Linux CI run `34337011949` at
`f003a0d7efbe1fb485815c219955777a9a8ecdbe` and merged as PR #110.
The following changes postdate that CI and the September 7 SYSTEM artifact:

- UnitStatus and winctl expose the manager substate and termination uncertainty.
  A public path Start/Stop with blocked native close verifies JSON status/list
  visibility, no diagnostic overwrite from a late failure, and successful cleanup.
- Explicit member publication now has documented, coherent state/substate pairs;
  failed watchdog causes remain retained. Beta oneshot active-after-success and
  repeated PathExists AND semantics are unchanged.
- Recovery admission independently rejects owned processes and unconfirmed cleanup.
  The gate-race regression now reaches recovery only after genuine exit cleanup.
- Start-limit accounting, notify partial-open retention, accepted configuration
  publication and user-host instance launch/cleanup/shutdown use lifecycle handlers.
  User launch liveness runs outside the host mutex; an independent logon test
  confirms progress while another liveness observation is delayed.

The mutex-serialized end-state is explicit in Design section 2. Timer/session
policy, broader admission guarantees, nonblocking aggregate observations and
immutable snapshots remain open. Per-resource cleanup accounting is a prerequisite
for R3 helper jobs, not a new beta behavior claim.

Validation: ten focused race repetitions passed, including the protocol status,
recovery, transition-pair and independent-user-logon tests. Existing user-host,
linger and admission tests passed with race detection. The uncached full local
Windows race suite and vet passed; the manager package took 45.1 seconds and the
full command 56.7 seconds. Hosted evidence is recorded in the associated PR.

## September 9 control transport admission

Each serving endpoint now caps accepted connections and concurrent handlers,
with independent stop and diagnostic capacity. Authenticated overload receives
`busy` before dispatch to the manager. Raw connection overflow closes immediately;
bounded request reads and response writes release stalled transport workers.
Handler completion does not acquire another admission slot. Standalone ServeConn
retains its already-admitted helper contract. OPERATIONS documents exact defaults
and the remaining hard-connection-cap limitation for remote stop access.

Twenty race-enabled protocol repetitions passed for ordinary/diagnostic overload,
reserved stop progress, rejected-request side-effect exclusion, slot reuse,
authorization precedence, hard connection overflow, idle reads, stalled responses,
and long handlers outliving request-read deadlines. This does not close #97:
end-to-end native worker bounds and timer/session admission remain open. It also
postdates the retained SYSTEM qualification.

## September 9 status observation lock boundaries

Status and list-units now capture process references alongside lifecycle fields
under the manager mutex, then observe process liveness/PID and journal counters
after unlocking. List-timers no longer performs unused process/journal observations.
UserHost Alive/Running likewise capture process references before observing them
outside the host mutex. Delayed observations cannot block unrelated stop or host
shutdown decisions. Responses remain best-effort overlays of captured lifecycle
fields and later external observations; immutable aggregate publication is open.

Twenty focused race repetitions passed for blocked status/list liveness with an
independent public stop and blocked user Alive/Running with a shutdown decision.
The uncached full Windows race suite and vet passed for the combined slice
(manager package: 43.5 seconds); exact hosted validation is recorded in the
associated PR. No newer SYSTEM
qualification is claimed.

## September 9 configuration graph planning

Reload captures definitions required by retained ownership, constructs and checks
its candidate graph outside m.mu, then validates the retention set and configuration
revision before publication. A concurrent start or completed cleanup invalidates
the candidate, including its errors, and causes a rebuild. Three invalidated
attempts return busy without changing accepted graph, availability or revision.
Enable/disable graph construction also runs outside m.mu; configMu protects its
accepted definition inputs while unrelated lifecycle work continues.

Twenty race-enabled repetitions passed for newly retained ownership, completed
cleanup, obsolete build errors, bounded rejection without publication, and stop
progress during enable graph planning. Existing reload/enable/disable regressions
also passed with race detection. The uncached full Windows race suite passed
(manager: 44.3 seconds), as did vet. Exact hosted evidence belongs to the
associated PR. Timer/session policy and immutable aggregate publication remain
open; this slice is not included in the retained SYSTEM qualification.

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
