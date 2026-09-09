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
