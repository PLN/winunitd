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
