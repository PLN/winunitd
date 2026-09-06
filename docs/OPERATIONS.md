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

An accepted RPC operation survives an ordinary client disconnect and can be
queried through a new connection. This does not yet replace existing operation
contexts with independent internal deadlines: manager/server cancellation and
direct in-process caller contexts keep their existing behavior. Explicit stop
and shutdown still control cleanup. Durable history, operation cancellation
commands, aggregate deadlines, and the remaining coordinator migration are open.

The protocol keeps its current version. IDs are optional JSON additions, and
`operation` is a new method. Existing methods retain their response shape and
error codes. Older servers reject the new query with `method-not-found`; the CLI
reports that failure rather than pretending that history is available. Queries
use the same authenticated manager pipe and authorization as other unit verbs.
