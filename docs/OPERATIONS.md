# Operation history

Start, explicit stop, and explicit restart transactions receive an opaque
`OperationID` after plan validation and admission. Successful CLI replies print
the ID; admitted failures include it in the RPC error and CLI diagnostic.
Participating units expose `LastOperationID` in status. Rejected requests do not
create an operation or change that field.

Concurrent explicit starts of the same unit share an admitted operation when
the runtime record, accepted configuration revision, and stop epoch still match.
A joined caller receives the same ID and its own reply copy. Cancelling that
caller's wait returns the ID for later queries and does not cancel the original
operation. A new revision or intervening stop prevents joining; trigger starts
and explicit restarts keep separate semantics. Joining does not consume another
start-transaction slot; each waiting RPC still occupies a control-handler slot.

```powershell
winctl restart app.target
winctl status app.target
winctl operation OPERATION_ID
```

The query reports the root unit, action, explicit/timer/watch origin, captured
configuration revision, running/succeeded/failed outcome, timestamps, and error.
Its exit codes are 0 for succeeded, 3 for running, 1 for failed or a transport
error, 2 for invalid CLI usage, and 4 for an unknown or evicted ID. The outcome is
the transaction result, not the unit's current lifecycle state or an inventory
of every dependency result. A later successful retry cannot turn an earlier
failed operation into a success.

History is private to each manager instance. Pending records are retained until
completion; the most recent 256 completed records are retained in completion
order. Errors in history are capped at 4,096 UTF-8 bytes with an explicit
truncation flag. A status ID can refer to an evicted record. Restarting the
manager loses history and creates a different ID namespace. There is no disk
archive, global history shared between user/system managers, or automatic
recovery-operation history yet.

Start/restart admission defaults to 32 transactions, including restart teardown.
Explicit stop has a separate default budget of 32 transactions. A full budget
rejects the request before adapter work; shutdown bypasses both budgets.
Embedders can override `Config.MaxStartTransactions` and
`Config.MaxStopTransactions`; the daemon currently uses the defaults. These
are transaction limits, not limits for all control connections or OS workers.

## Accepted work and cancellation

This section describes development builds after `0.2.1-beta`. The released MSI
does not yet include these operation-lifetime changes.

After admission, start, stop and restart use manager-owned contexts. Cancelling
the first caller, a joined caller, or the serving connection ends only that
response wait. The caller receives an operation ID when the response channel is
still available; after disconnect, status supplies LastOperationID for a new
connection to query. A request already canceled before admission creates no
operation. In-process callers use the same rule; StopContext is the cancellable
wait form of Stop.

One internal budget spans the whole captured operation, including ordered members,
unit-gate waits and both restart phases. The daemon derives it from the serialized
sum of member allowances, with a five-minute minimum. Service starts contribute
the larger of 30 seconds or TimeoutStartSec. All start and stop members additionally
contribute two TimeoutStopSec allowances each. Unspecified stop allowances
are five seconds. A further five seconds covers manager overhead. This conservative
sum allows configured long phases without shortening them to the minimum.
Embedders may set a positive Config.OperationTimeout to override it; negative
values are rejected. No unit-file directive or installer setting is added.

The budget uses the manager's monotonic clock. Optional DeadlineAt is its wall-time
projection at admission; a later wall-clock change does not reset the budget.
Optional CancellationReason records internal cancellation. Both appear in
`winctl operation` output and JSON operation queries. Caller-wait cancellation
does not set CancellationReason because the operation continues.

Expiry returns failure to waiters but is not proof of process termination. History
can remain running with a cancellation reason while a native launch returns;
the admitted transaction retains its slot and runtime records. Late creations are
adopted and cleaned up under their exact invocation generation, with a fresh
TimeoutStopSec cleanup allowance. Already completed members keep their results
and running workloads, as with an ordinary partial start failure.

A final failed operation may still leave uncertain unit cleanup: timed-out native
stop attempts retain their resource ownership and retries join pending calls.
Inspect status and retry stop; neither a timeout nor a failed history entry grants
permission to replace live files. Transaction budgets do not bound every native
worker; that broader admission work remains R2.3. Control transport limits are
described below.

Unit status derives `terminationUncertain` from separate pending resource classes.
`pendingCleanup` identifies `workload` (process/job or native proxy),
`notification`, `watch`, `stop-helper`, and `journal`. Each exact-owner completion releases only its own
class. For example, confirmed process termination releases the process reference
even if notification close fails; that listener remains owned and blocks restart
until cleanup succeeds. Successful watch close cannot erase notification failure.
Explicit stop retries the remaining owned resources.

Output buffering admits at most 16 MiB of message bytes and 16,384 records across
the store, including its in-flight write. Each invocation is limited to 4 MiB and
12,288 records. At most 2048 invocation queues can wait behind the writer. The
writer takes one record per invocation in rotation, preserving each invocation's
order. At aggregate saturation a smaller queue may displace newest pending
records from a larger queue; the victim's dropped-byte/record counters include
that loss. At least one pending record per victim is preserved. If no eligible
victim exists, or an invocation/group limit is reached, new output is discarded
and counted. This provides progress after storage recovers, not lossless capture
or a bound on filesystem latency. Journal files and retention have separate limits.

For process-backed units, `mainPid` is captured at adoption and copied together
with lifecycle/invocation state. It clears when the process reference is released.
Status and list-units do not query process liveness; a PID retained during failed
cleanup identifies the owned invocation and does not establish that its main
process is still running. Pending `journal` cleanup means a capture or its flush
has not completed; it can coexist with `mainPid=0` after confirmed process exit.
An expired wait retains the same capture. A replacement process is not created
until prior output completes, and failed cleanup requires an explicit stop retry.
Manager close also joins main captures and reports failure while output remains
unresolved. Native proxy, timer and journal observations remain
separate from that manager decision snapshot.

Disk-backed unit verification and reload limit each file to 64 KiB. A reload reads
at most 1024 unit files and admits at most 1024 runtime records, including built-in
targets and removed definitions still needed for cleanup. Stop retained workloads
before retrying a replacement that would exceed this allowance. The unit directory
is limited to 4096 entries; enabled-link discovery has a separate 4096-entry budget
across its root and immediate target directories, including ignored entries.
Disable validates that entire bounded namespace before removing enable records;
it does not traverse or delete files in ignored nested directories. Enumeration
failure leaves files unchanged. A later deletion failure can still leave a
partially disabled unit and is reported to the caller.
Oversized candidates leave the accepted graph, revisions and ownership unchanged.
Reload reports at most 128 error messages totaling 128 KiB, plus an explicit
omission notice. Correct the reported errors and reload to see remaining errors.
These are input/record bounds, not a guarantee of bounded filesystem latency or
aggregate process/output memory.

`winctl snapshot` (also `--user`) prints a JSON copy of one manager's accepted
unit states and active operations, captured together under its decision lock.
It includes manager identity, capture sequence/time, machine counts, configuration
and invocation identities, owned PIDs, cleanup classes and operation IDs. Sequence
orders captures within that manager lifetime; it is not a lifecycle revision.
Later completions and reloads cannot alter a published result, and editing the
returned data cannot mutate manager state.

Snapshot performs no native queries or worker dispatch. It excludes journal,
timer-engine, native-proxy and separate user-host observations; ordinary status
commands provide those independent views. Its armed revision covers the timer or
watch arm accepted in the manager record. Completed operation
history remains available through `operation ID`. A snapshot exceeding 1024 units,
128 active operations or 512 KiB fails explicitly; it never truncates a complete
view into apparent success. Use individual status/operation queries above those
bounds. Snapshot captures do not retain additional history in the manager.

Accepted stop and restart invalidate pending activation and suppress recovery for
their complete captured stop scope before dispatching ordered teardown workers.
Plan validation and capacity rejection happen before this change in eligibility. Manager shutdown/close also cancels accepted starts and
restarts. Already accepted stops retain their own bounded cleanup lifetime, and
shutdown can join their pending native calls. Durable history, coordinator-wide cancellation coverage and the remaining
migration stay open.

The protocol keeps its current version. IDs and deadline/cancellation metadata are optional JSON additions, and
`operation` is a new method. Existing methods retain their response shape; transport overload adds the
`busy` error code. Older servers reject the new query with `method-not-found`; the CLI
reports that failure rather than pretending that history is available. Queries
use the same authenticated manager pipe and authorization as other unit verbs.

## Control transport capacity

Development builds bound each serving endpoint to 128 accepted connections,
64 ordinary handlers, eight stop/disable-linger/cancel-operation handlers, and eight diagnostic
handlers (status, operation, list-units and list-timers). These budgets are per
endpoint, separate from manager transaction budgets. Embedders can use
`protocol.ServeWithLimits`; the daemon uses the defaults. The standalone
`ServeConn` helper assumes an already-admitted connection and adds no limits.

Authenticated requests beyond their handler class receive `busy` before the
manager handler runs, so rejection does not create an operation or change unit
state. Stop and diagnostic slots cannot be consumed by ordinary requests.
Accepted handlers finish without acquiring another transport slot, and manager
shutdown bypasses transport admission. Slots remain occupied until handlers
actually return, even after a client disconnects.

At the hard connection cap, new connections close without a protocol response.
Idle or incomplete requests have a 30-second read deadline; response writes have
a five-second deadline. Neither deadline limits handler execution. Persistent
clients must reconnect after idle expiry. Authentication/native calls that stall
still retain their connection slots; deadlines do not forcibly terminate them.
Raw connections can therefore exhaust the hard cap even when reserved handler
slots are free. These bounds limit resource growth and isolate handler classes;
they do not guarantee remote stop access under connection-flood overload.

Reload constructs candidate dependency graphs outside the lifecycle mutex and
rechecks ownership before accepting them. If concurrent lifecycle changes
invalidate three consecutive candidates, daemon-reload returns `busy`; retry the
command. This rejection leaves the accepted graph and configuration revision
unchanged. Enable/disable planning also allows lifecycle work to continue while
configuration changes remain serialized.

## Explicit cancellation

Development builds add `winctl cancel OPERATION_ID` (`cancel-operation` RPC),
using the same authenticated system/user pipe as operation queries. It cancels
unfinished start/restart work. Successfully completed members remain running,
matching deadline expiry and partial-start failure; shared start callers all
receive the same cancellation outcome. No dependency rollback is performed.

For example, if db.service started successfully and web.service is still waiting
for readiness, cancel cleans up the unfinished web invocation and leaves db
running. Cancel during restart teardown prevents its subsequent start phase;
ongoing native cleanup stays owned and may still need an explicit stop retry.
Accepted explicit stop operations reject cancellation and keep their cleanup
budget. Completed operations return their unchanged outcome, and repeated
cancellation preserves the first cancellation reason.

The response is a copy of the current operation record. Running with a
CancellationReason means cancellation was requested and cleanup may remain;
it does not confirm termination. Late native creations remain tracked and cleaned
up. Use `operation` and unit status to inspect completion/uncertainty. Exit codes
match `operation`: 0 succeeded, 3 running, 1 failed or RPC error, 2 invalid usage,
and 4 unknown/evicted ID. During the short completion-publication boundary,
cancel can return busy; query or retry. Older servers return method-not-found.
Cancellation uses the reserved stop handler budget and allocates no worker.

## Global maintenance

Development builds provide an administrator-only system-manager command:

```powershell
winctl maintenance --timeout 180s
winctl status
```

The default and maximum aggregate deadline is 180 seconds; the CLI accepts
durations from 1ms through 180s. The `maintenance` RPC accepts `timeoutMS`, with
zero selecting the default. It closes system/user admission together, cancels
activation/restart work, stops owned workloads, and drains tracked token,
configuration, native-control and journal cleanup. Status and other diagnostics
remain available. `--user` is rejected; owning a user pipe does not authorize
global maintenance. Older servers without the required endpoint or RPC fail;
missing support is never treated as successful quiescence.

The CLI uses the dedicated `\\.\pipe\winunitd\maintenance` endpoint with the
same protected administrator/SYSTEM DACL and server-identity verification as
system control. Its eight connection slots are independent of ordinary control;
it permits only maintenance, with four concurrent request slots and five-second
idle-read/write timeouts. Both listeners must open before SCM readiness. Failure
of either listener cancels both serving loops. Saturating ordinary connections
cannot consume maintenance capacity. Saturating the maintenance endpoint itself
can still cause transport failure or busy responses; its limits are not a denial
of service guarantee against an administrator. Status and unit commands continue
to use ordinary control. The CLI does not fall back to its saturated transport.

The accepted attempt owns its deadline independently of the requesting
connection. Disconnecting does not cancel it. Concurrent requests join the same
attempt without changing its deadline. Machine status exposes `maintenance`
with `quiescing`, `failed`, or `quiesced`, start/deadline timestamps, and the last
attempt's error. After failure, another maintenance request retries retained work
with a new deadline. Pending native calls remain owned and are joined, not
duplicated. The CLI exits 0 only after an explicit `quiesced` result, 1 for
incomplete work or transport/protocol failure, and 2 for invalid usage.

Admission stays closed after success or failure. Restart the manager process to
resume enabled workloads; there is no in-process resume command. Manually started,
non-enabled units are not promised restoration. Maintenance does not rewrite
enable records or roll back configuration mutations admitted before the barrier.

`quiesced` confirms workload/resource cleanup while the broker and control pipe
remain running. Before replacing broker binaries, servicing must also stop the
SCM service and confirm its process has exited. A failed or missing maintenance
confirmation must abort replacement. This command is the runtime primitive;
MSI servicing and rollback qualification remain separate release gates.

Maintenance uses its own protected endpoint and bounded connection/handler
capacity. Ordinary control-pipe saturation cannot consume those slots; saturation
or failure of the maintenance endpoint itself can still reject a new request.

## Headless user managers

Local-machine lingering users run in session zero. Their profile lifetime belongs
to Windows through CreateProcessWithTokenW(LOGON_WITH_PROFILE), so abrupt broker
death does not leave a manually loaded profile reference. The manager obtains its
environment from that profile and selects the profile working directory after
startup. S4U does not supply cached outbound credentials; optional credential-store
modes remain outside the qualified release contract.

A separate SYSTEM helper, using the same daemon executable, holds a private
window station and desktop restricted to SYSTEM and the target SID. It is an
owned process under the broker job, and never starts a user manager itself.
The broker keeps helper cleanup and the bounded readiness read until they finish.
The helper's internal command-line mode is not an operator startup interface.
Existing station names are rejected instead of reusing their permissions.

Headless launch requires a session-zero SYSTEM broker in its verified root job.
The suspended user process must inherit that job and join its dedicated job
before resuming. Interactive managers continue to use WTS tokens and Windows'
interactive profile ownership. Managed profiles currently require local accounts.

System status includes `userInstances`: the user-host's last accepted per-SID
state, selected launch mode (`interactive`, `headless-s4u`, or experimental
`headless-store-uri`), selected session ID, manager PID, and current interactive
session count. These copied decisions are updated by reconciliation; they are
not a fresh kernel liveness query. PID/session observations do not authorize
process termination. Recovery entries retain their retry/error diagnostics.

Linger decisions and counts use the last validated record snapshot. Filesystem
scans and mutations are serialized outside the decision lock and remain tracked
through shutdown. `lingerState` is `pending` before the initial scan, `ready`
after a valid scan, or `degraded` with `lingerError` after a failed observation.
Invalid or unreadable records lose cached authority; reconciliation requests
cleanup unless an interactive session independently permits the manager.
Records are limited to 64 KiB each, 128 grants, and 4096 directory entries.
An oversized directory is rejected as a whole. External edits take effect on
the next reconciliation; successful control mutations update the snapshot before
returning. A token obtained for a superseded grant cannot start a manager.

A later logon or linger toggle does not replace a running manager's chosen mode.
An explicit manager restart selects a fresh suitable token. An interactive
manager may still report its original selected session after that session leaves;
this is distinct from the current session count. Cleanup removes its record.

## Timer storage work

Timer arms load persisted state asynchronously. Status and `list-timers` report
storage as `loading`, `ready`, or `failed`; a timer cannot dispatch until its
state is loaded. One reserved loader serves at most 1024 armed timers, with at
most one indexed heap deadline per arm. Reschedule/disarm removes the previous
entry immediately, so repeated activity cannot accumulate stale deadlines. The 32
callback slots include pre-dispatch persistence and completion writes. Storage
calls are serialized outside the scheduler and manager decision locks, and all
shutdown callers join accepted storage work.

One reserved calendar worker computes deadlines outside decision locks. Each arm
coalesces pending changes; stale arm or clock generations cannot publish results.
Timers accept at most 64 `OnCalendar` expressions. Schedule status is `planning`
until a deadline is accepted, `ready` after calculation, `waiting` during storage
load, or `failed` after a storage error. While planning, Next is absent; status
reads copy accepted state without doing calendar searches. Relative-only timers
and admission retries remain independent of the calendar worker. Shutdown joins
accepted calculations.

An occurrence is written before dispatch. A failed read or write suspends further
dispatch for that arm and exposes the storage error. Repair the storage problem
and stop/start the timer to reload its state. Complete files replace previous
state only after write, flush and close succeed. State is bounded to 16 KiB and
uses version 2; legacy unversioned and version 1 timestamps are read and upgraded on the next
save. Unsupported versions, malformed JSON and invalid timestamps fail visibly.

Persistent calendar timers write a pending activation ID, target and timestamps
before dispatch, then record activation success or failure. An interrupted
pending record is retried once on the next arm with the same ID. The retry
coalesces calendar occurrences missed through its dispatch time. A completed
result is not replayed; admission overload retains the same pending intent.
Activation success means the start request completed, not that a long-running
workload later exited successfully. Status and timer lists expose this record.

A crash after execution but before its result becomes durable can cause duplicate
execution. These workloads must be idempotent. A pending record whose target or
persistent-calendar mode no longer matches configuration suspends dispatch;
restore the matching configuration or explicitly remove the stopped timer's
state to discard that intent. Stop/disarm prevents further dispatch in the current
arm; a later explicit arm can recover its pending intent.

Older version 1 readers reject version 2 state. Preserve state backups for any
downgrade to an older reader; installer rollback qualification remains R6 work.
Aggregate decision snapshots remain separate R2/R5 work.
