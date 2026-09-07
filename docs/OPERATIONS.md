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
start-transaction slot, though control-connection limits remain separate work.

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
worker or control connection; that broader admission work remains R2.3.

Explicit stop continues to invalidate pending activation for its scope before
acquiring the unit gate. Manager shutdown/close also cancels accepted starts and
restarts. Already accepted stops retain their own bounded cleanup lifetime, and
shutdown can join their pending native calls. Durable history, a separate operation
cancellation command and the remaining coordinator migration stay open.

The protocol keeps its current version. IDs and deadline/cancellation metadata are optional JSON additions, and
`operation` is a new method. Existing methods retain their response shape and
error codes. Older servers reject the new query with `method-not-found`; the CLI
reports that failure rather than pretending that history is available. Queries
use the same authenticated manager pipe and authorization as other unit verbs.
